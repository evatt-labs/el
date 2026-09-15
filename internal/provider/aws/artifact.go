package aws

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"

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
// # Exclusions
//
// Not every file under dir belongs in the package. Three things are cut
// before a single byte is read, in this precedence order:
//
//  1. Two unconditional denies, regardless of dir's .gitignore or include:
//     — ".git" (never appears in a .gitignore's own vocabulary, and must
//     never ship) and any path whose basename matches ".env*" (defence in
//     depth: a credential reaching the artifact must not depend on a
//     manifest author having remembered to gitignore it). See
//     isUnconditionallyDenied. Never overridable.
//  2. dir's own .gitignore, if one exists, via go-git's
//     plumbing/format/gitignore subpackage — negation, directory-only
//     patterns, anchoring and "**" all need real gitignore semantics to
//     get right, which is exactly why this is not hand-rolled. See
//     newArtifactExcluder.
//  3. include, which re-adds a path .gitignore excluded (manifest.Compute.
//     Include's own doc comment has the "why": .gitignore is not a
//     deployment manifest, and a service's own build output is routinely
//     gitignored precisely because it must never be committed, yet is
//     exactly what the deployed artifact needs). include can never re-add
//     what (1) denies — see newArtifactExcluder.
//
// An excluded directory is pruned with fs.SkipDir rather than walked and
// filtered entry-by-entry: this is also what keeps a symlink inside an
// excluded directory (a Python virtualenv's bin/python, say) from ever
// reaching the symlink check below — buildArtifact never looks inside a
// directory it has already decided not to package.
//
// # Determinism
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
//     today." Exclusions do not threaten this: they run during the walk,
//     before sorting, so the sorted set they leave behind is exactly as
//     order-independent as an unfiltered one.
//   - Per-entry mtime: fixed to deterministicZipTime (see its own doc
//     comment) rather than the real file mtime, which varies with checkout
//     time, clone method and OS.
//   - Per-entry mode: fixed to 0o644 for a regular file, 0o755 for a
//     directory (directories are not written as entries at all — see
//     below) — a real filesystem's mode bits (umask, an executable bit
//     some checkouts preserve and others don't) are exactly the kind of
//     host-dependent noise this function exists to strip.
//
// A symlink that survives every exclusion above is rejected rather than
// silently followed or silently skipped: a build that quietly changes
// shape depending on whether a symlink target exists on the machine
// running it is not deterministic, and Rule 20 rules out resolving that
// ambiguity by guessing.
func buildArtifact(dir string, include []string) (data []byte, sha256Hex string, err error) {
	type entry struct {
		relPath string
		absPath string
	}
	var entries []entry

	excluder, excludeErr := newArtifactExcluder(dir, include)
	if excludeErr != nil {
		return nil, "", excludeErr
	}

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			// The root itself is never a candidate entry and never subject
			// to exclusion — only what's under it.
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		relSlash := filepath.ToSlash(rel)
		components := strings.Split(relSlash, "/")

		if excluder.excluded(components, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return kerrors.Validation("artifact source %q contains a symlink at %q, which is not supported", dir, path)
		}
		entries = append(entries, entry{relPath: relSlash, absPath: path})
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

// gitignoreFileName is the one file buildArtifact's exclusion logic reads:
// dir's own .gitignore, at dir's root only. Not the wider repository's
// .gitignore (dir may be a subdirectory of a larger checkout, e.g. a
// monorepo service directory) and not a .gitignore nested further down dir
// — kraai's artifact excludes are a packaging concern scoped to the one
// directory being packaged, not a full git-worktree reimplementation.
const gitignoreFileName = ".gitignore"

// artifactExcluder decides, for each path buildArtifact's walk visits,
// whether it belongs in the deployment package. See buildArtifact's own
// doc comment for the three-rule precedence this implements.
type artifactExcluder struct {
	matcher gitignore.Matcher
}

// newArtifactExcluder builds an artifactExcluder for dir: dir's own
// .gitignore (absent is not an error — a service with no .gitignore simply
// gets the two unconditional denies and nothing else) as the base pattern
// set, with include appended as forced-inclusion patterns.
//
// include's patterns are appended, not prepended: gitignore.Matcher checks
// patterns from last to first and stops at the first match (go-git's own
// NewMatcher doc: "Patterns must be given in the order of increasing
// priority"), so appending is what makes include win over a conflicting
// .gitignore pattern. It cannot win over the two unconditional denies
// checked in excluded below, because those never consult this matcher at
// all — there is no pattern here for an include: entry to outrank.
func newArtifactExcluder(dir string, include []string) (*artifactExcluder, error) {
	patterns, err := readGitignorePatterns(dir)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "reading %s for artifact source %q", gitignoreFileName, dir)
	}
	for _, inc := range include {
		inc = strings.TrimSpace(inc)
		if inc == "" {
			continue
		}
		if !strings.HasPrefix(inc, "!") {
			inc = "!" + inc
		}
		patterns = append(patterns, gitignore.ParsePattern(inc, nil))
	}
	return &artifactExcluder{matcher: gitignore.NewMatcher(patterns)}, nil
}

// excluded reports whether relComponents (dir-relative path, split on "/")
// should be left out of the artifact. isDir must reflect whether the path
// itself is a directory: gitignore's directory-only patterns (a trailing
// "/" in .gitignore) only match when isDir is true, matching
// gitignore.Matcher.Match's own contract.
//
// The unconditional check runs first and, if it fires, is the whole
// answer: dir's .gitignore and include are never consulted for a path
// this denies, which is what makes the deny actually unconditional rather
// than merely default-on.
func (a *artifactExcluder) excluded(relComponents []string, isDir bool) bool {
	basename := relComponents[len(relComponents)-1]
	if isUnconditionallyDenied(basename) {
		return true
	}
	return a.matcher.Match(relComponents, isDir)
}

// isUnconditionallyDenied reports whether basename is denied regardless of
// dir's .gitignore or include: — see buildArtifact's own doc comment for
// why these two, specifically, are never overridable: ".git" never appears
// in a .gitignore's own vocabulary and must never ship regardless, and
// ".env*" is the credential-leak guard itself — the one exclusion this
// change must not let a forgetful or mistaken include: entry undo.
//
// Checked against a path's basename alone, not its full relative path:
// buildArtifact prunes an excluded directory with fs.SkipDir before
// descending into it, so by the time any entry is checked here, every
// denied ancestor directory has already been pruned — there is nothing
// left for a full-path check to catch that a basename check would miss.
func isUnconditionallyDenied(basename string) bool {
	if basename == gitignoreDenyGitDir {
		return true
	}
	matched, matchErr := filepath.Match(gitignoreDenyEnvGlob, basename)
	return matchErr == nil && matched
}

// gitignoreDenyGitDir and gitignoreDenyEnvGlob are isUnconditionallyDenied's
// two rules, named rather than inlined so the "these two, only these two"
// claim in its doc comment is something a reader (or a future diff) can
// verify at a glance.
const (
	gitignoreDenyGitDir  = ".git"
	gitignoreDenyEnvGlob = ".env*"
)

// readGitignorePatterns reads dir's own .gitignore, if one exists, and
// parses each non-blank, non-comment line into a gitignore.Pattern.
//
// A nil domain: dir is the packaging root, so every pattern is anchored
// (in gitignore's own sense of "anchored" — i.e. not anchored at all,
// matching git's default of an unanchored pattern matching at any depth)
// relative to dir itself, exactly as .gitignore's own semantics already
// mean for a .gitignore living at the root of what it governs.
//
// The read-and-split logic mirrors go-git's own dir.go readIgnoreFile line
// for line (blank/comment detection, ParsePattern per surviving line),
// deliberately hand-rolled here rather than calling that function
// directly. dir.go's readIgnoreFile takes a billy.Filesystem, not a plain
// path, and does considerably more than this function needs or wants: it
// walks core.excludesfile from gitconfig, /etc/gitconfig, and
// $HOME-relative paths, chasing the full precedence chain a real git
// checkout resolves .gitignore/.gitconfig through. This function wants
// exactly one file, dir's own .gitignore, nothing else — reusing
// readIgnoreFile would mean either adapting a billy.Filesystem wrapper
// around dir for no benefit, or accepting config-file lookups this
// packaging step has no business performing. Note this does not avoid a
// go-billy dependency at the module level: pattern.go and matcher.go
// (ParsePattern and NewMatcher, used throughout this file) import only
// stdlib, but they live in the same gitignore package as dir.go, and Go
// compiles a package's files together — importing this package at all
// pulls go-billy, gcfg and go-git's own config/ioutil packages into the
// build regardless of whether dir.go's functions are ever called. Verified
// via `go list -deps` against a throwaway import of exactly the two
// functions this file uses; see this package's PR description.
func readGitignorePatterns(dir string) ([]gitignore.Pattern, error) {
	data, err := os.ReadFile(filepath.Join(dir, gitignoreFileName)) //nolint:gosec // dir is a caller-supplied service directory, not untrusted input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var patterns []gitignore.Pattern
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		patterns = append(patterns, gitignore.ParsePattern(line, nil))
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, scanErr
	}
	return patterns, nil
}
