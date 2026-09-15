package aws

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// writeTree creates files (path -> content) under a fresh temp directory
// and returns its root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return dir
}

// TestBuildArtifactDeterministic is the brief's own required property test:
// building the same source tree twice must produce byte-identical zips and
// identical hashes, even when the two builds happen at different times
// (real-world equivalent of two `kraai apply` runs on unchanged code).
func TestBuildArtifactDeterministic(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py":      "def handler(event, context):\n    return {'ok': True}\n",
		"app/util.py":      "VALUE = 42\n",
		"requirements.txt": "fastapi==0.115.0\n",
	})

	data1, sha1, err := buildArtifact(dir)
	if err != nil {
		t.Fatalf("buildArtifact (first): %v", err)
	}

	// A real mtime difference between the two builds — the clock genuinely
	// advances, and on some filesystems a rewrite bumps mtime — must not
	// leak into the archive.
	time.Sleep(2 * time.Millisecond)
	for rel := range map[string]string{"app/main.py": "", "app/util.py": "", "requirements.txt": ""} {
		now := time.Now()
		if err := os.Chtimes(filepath.Join(dir, rel), now, now); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
	}

	data2, sha2, err := buildArtifact(dir)
	if err != nil {
		t.Fatalf("buildArtifact (second): %v", err)
	}

	if sha1 != sha2 {
		t.Fatalf("sha256 differs across builds of identical content: %s vs %s", sha1, sha2)
	}
	if !bytes.Equal(data1, data2) {
		t.Fatal("zip bytes differ across builds of identical content")
	}
}

// TestBuildArtifactContentChangeChangesHash proves the hash is not a
// constant — buildArtifact must actually reflect the input, or the
// determinism test above would be vacuous.
func TestBuildArtifactContentChangeChangesHash(t *testing.T) {
	dir1 := writeTree(t, map[string]string{"app/main.py": "return 1\n"})
	dir2 := writeTree(t, map[string]string{"app/main.py": "return 2\n"})

	_, sha1, err := buildArtifact(dir1)
	if err != nil {
		t.Fatalf("buildArtifact(dir1): %v", err)
	}
	_, sha2, err := buildArtifact(dir2)
	if err != nil {
		t.Fatalf("buildArtifact(dir2): %v", err)
	}
	if sha1 == sha2 {
		t.Fatal("different content produced the same hash")
	}
}

// TestBuildArtifactFileOrderIndependent proves determinism holds
// regardless of filesystem traversal/creation order, not just when files
// happen to already be walked alphabetically.
func TestBuildArtifactFileOrderIndependent(t *testing.T) {
	dirA := writeTree(t, map[string]string{
		"z.py": "z\n",
		"a.py": "a\n",
		"m.py": "m\n",
	})
	// Same content, deliberately created in a different order on disk.
	dirB := t.TempDir()
	for _, rel := range []string{"m.py", "z.py", "a.py"} {
		content := map[string]string{"z.py": "z\n", "a.py": "a\n", "m.py": "m\n"}[rel]
		if err := os.WriteFile(filepath.Join(dirB, rel), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	_, shaA, err := buildArtifact(dirA)
	if err != nil {
		t.Fatalf("buildArtifact(dirA): %v", err)
	}
	_, shaB, err := buildArtifact(dirB)
	if err != nil {
		t.Fatalf("buildArtifact(dirB): %v", err)
	}
	if shaA != shaB {
		t.Fatalf("hash depends on directory creation order: %s vs %s", shaA, shaB)
	}
}

// TestBuildArtifactZipContentsAreDeterministic unzips the built artifact
// and checks every entry's stored mtime is the fixed epoch, and that entry
// order is sorted — the two knobs buildArtifact's own doc comment says it
// normalises, verified directly rather than only through the hash.
func TestBuildArtifactZipContentsAreDeterministic(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"b.py": "b\n",
		"a.py": "a\n",
	})

	data, _, err := buildArtifact(dir)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if !f.Modified.Equal(deterministicZipTime) {
			t.Errorf("entry %q Modified = %v, want %v", f.Name, f.Modified, deterministicZipTime)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("entry names not sorted: %v", names)
	}
	if len(names) != 2 {
		t.Fatalf("names = %v, want 2 entries", names)
	}
}

// TestBuildArtifactRejectsSymlink proves a symlinked source file fails
// loudly (Rule 20) rather than being silently followed or silently
// skipped, either of which would make the artifact's shape depend on
// whatever the symlink happened to resolve to on the machine running the
// build.
func TestBuildArtifactRejectsSymlink(t *testing.T) {
	dir := writeTree(t, map[string]string{"real.py": "x\n"})
	if err := os.Symlink(filepath.Join(dir, "real.py"), filepath.Join(dir, "link.py")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	if _, _, err := buildArtifact(dir); err == nil {
		t.Fatal("expected an error for a source tree containing a symlink")
	}
}

// TestBuildArtifactRejectsEmptyDir proves an empty source directory is a
// validation error, not a valid zero-file zip an operator would only
// notice was wrong once Lambda refused it.
func TestBuildArtifactRejectsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := buildArtifact(dir); err == nil {
		t.Fatal("expected an error for an empty source directory")
	}
}

// TestBuildArtifactContentIsReadable proves the produced archive is not
// merely deterministic but actually round-trips the real file contents —
// a determinism bug that also corrupted content would otherwise pass every
// test above.
func TestBuildArtifactContentIsReadable(t *testing.T) {
	dir := writeTree(t, map[string]string{"app/main.py": "hello world\n"})

	data, _, err := buildArtifact(dir)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("got %d entries, want 1", len(zr.File))
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close() //nolint:errcheck // read-only zip entry reader in a test; nothing left to report
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hello world\n" {
		t.Fatalf("content = %q, want %q", got, "hello world\n")
	}
}
