package aws

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// deterministicZipTime is the fixed modification time every entry in a
// built artifact carries, in place of the real filesystem mtime.
//
// zip's format stores each entry's mtime in the archive bytes themselves;
// two builds of byte-identical source files on two different days, or two
// different machines with different checkouts, would otherwise produce two
// different zips — and, downstream, two different content hashes, so every
// apply would look like a code change even when nothing changed. DOS epoch
// (1980-01-01, zip's own minimum representable time) rather than the Unix
// epoch: archive/zip predates zip64 extended timestamps in its default
// writer and silently clamps an out-of-range time to this value anyway, so
// naming it explicitly documents the behavior instead of relying on a
// zero-value time.Time to fall into it by accident.
var deterministicZipTime = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

// buildArtifact walks dir and produces a deterministic zip deployment
// package plus the lowercase hex SHA-256 of the resulting bytes.
//
// Determinism requires normalising everything the zip format itself is
// willing to vary between two byte-identical source trees:
//
//   - File order: filepath.WalkDir's own traversal order is already
//     lexical per directory level, but this still sorts the collected
//     relative paths explicitly rather than depending on that being true
//     of every OS/filesystem WalkDir might run on — the contract this
//     function needs (same input bytes in, same output bytes out,
//     regardless of host) is stronger than "WalkDir happens to be sorted
//     today."
//   - Per-entry mtime: fixed to deterministicZipTime (see its own doc
//     comment) rather than the real file mtime, which varies with checkout
//     time, clone method and OS.
//   - Per-entry mode: fixed to 0o644 for a regular file, 0o755 for a
//     directory (directories are not written as entries at all — see
//     below) — a real filesystem's mode bits (umask, an executable bit
//     some checkouts preserve and others don't) are exactly the kind of
//     host-dependent noise this function exists to strip.
//
// Symlinks are rejected rather than silently followed or silently skipped:
// a build that quietly changes shape depending on whether a symlink target
// exists on the machine running it is not deterministic, and Rule 20 rules
// out resolving that ambiguity by guessing.
func buildArtifact(dir string) (data []byte, sha256Hex string, err error) {
	type entry struct {
		relPath string
		absPath string
	}
	var entries []entry

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return kerrors.Validation("artifact source %q contains a symlink at %q, which is not supported", dir, path)
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		entries = append(entries, entry{relPath: filepath.ToSlash(rel), absPath: path})
		return nil
	})
	if walkErr != nil {
		return nil, "", kerrors.Wrap(walkErr, kerrors.CodeUnexpected, "walking artifact source %q", dir)
	}
	if len(entries) == 0 {
		return nil, "", kerrors.Validation("artifact source %q contains no files", dir)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].relPath < entries[j].relPath })

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for _, e := range entries {
		content, readErr := readFileLimited(e.absPath)
		if readErr != nil {
			return nil, "", kerrors.Wrap(readErr, kerrors.CodeUnexpected, "reading %q for artifact", e.absPath)
		}

		header := &zip.FileHeader{
			Name:     e.relPath,
			Method:   zip.Deflate,
			Modified: deterministicZipTime,
		}
		header.SetMode(0o644)

		w, createErr := zw.CreateHeader(header)
		if createErr != nil {
			return nil, "", kerrors.Wrap(createErr, kerrors.CodeUnexpected, "adding %q to artifact", e.relPath)
		}
		if _, writeErr := w.Write(content); writeErr != nil {
			return nil, "", kerrors.Wrap(writeErr, kerrors.CodeUnexpected, "writing %q to artifact", e.relPath)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", kerrors.Wrap(err, kerrors.CodeUnexpected, "finalizing artifact for %q", dir)
	}

	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

// maxArtifactFileBytes bounds a single source file this package will read
// into memory while building an artifact — a backstop against an
// accidentally-enormous file (a checked-in dataset, a build cache directory
// pointed at by mistake) silently ballooning process memory, the same
// defensive-bound spirit as client.go's maxListPages.
const maxArtifactFileBytes = 256 << 20 // 256 MiB

func readFileLimited(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // path is walked from a caller-supplied service directory, not untrusted user input
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only fd; nothing left to report if Close fails after a successful read

	limited := io.LimitReader(f, maxArtifactFileBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(data) > maxArtifactFileBytes {
		return nil, kerrors.Validation("%q exceeds the %d byte artifact source file limit", path, maxArtifactFileBytes)
	}
	return data, nil
}
