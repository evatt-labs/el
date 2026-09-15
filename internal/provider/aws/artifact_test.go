package aws

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

	data1, sha1, err := buildArtifact(dir, nil)
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

	data2, sha2, err := buildArtifact(dir, nil)
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

	_, sha1, err := buildArtifact(dir1, nil)
	if err != nil {
		t.Fatalf("buildArtifact(dir1): %v", err)
	}
	_, sha2, err := buildArtifact(dir2, nil)
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

	_, shaA, err := buildArtifact(dirA, nil)
	if err != nil {
		t.Fatalf("buildArtifact(dirA): %v", err)
	}
	_, shaB, err := buildArtifact(dirB, nil)
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

	data, _, err := buildArtifact(dir, nil)
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

	if _, _, err := buildArtifact(dir, nil); err == nil {
		t.Fatal("expected an error for a source tree containing a symlink")
	}
}

// TestBuildArtifactRejectsEmptyDir proves an empty source directory is a
// validation error, not a valid zero-file zip an operator would only
// notice was wrong once Lambda refused it.
func TestBuildArtifactRejectsEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := buildArtifact(dir, nil); err == nil {
		t.Fatal("expected an error for an empty source directory")
	}
}

// TestBuildArtifactContentIsReadable proves the produced archive is not
// merely deterministic but actually round-trips the real file contents —
// a determinism bug that also corrupted content would otherwise pass every
// test above.
func TestBuildArtifactContentIsReadable(t *testing.T) {
	dir := writeTree(t, map[string]string{"app/main.py": "hello world\n"})

	data, _, err := buildArtifact(dir, nil)
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

// zipNames returns the entry names in a built artifact — shared by every
// exclusion test below to assert exactly what did or did not make it into
// the package.
func zipNames(t *testing.T, data []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestBuildArtifactExcludesDotEnvUnconditionally is the brief's own single
// most important test: a credential file must never reach the artifact,
// even when the service directory's .gitignore says nothing about it —
// exactly kraai-api's own real .env, kept out of git by hand rather than
// by a .gitignore entry. See this package's PR description for the
// revert-and-fail evidence that this test actually catches a regression
// here, not just a green checkmark nobody has seen fail (Rule 7).
func TestBuildArtifactExcludesDotEnvUnconditionally(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py": "def handler(): pass\n",
		".env":        "NEON_API_KEY=super-secret\n",
	})

	data, _, err := buildArtifact(dir, nil)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	if containsName(names, ".env") {
		t.Fatalf(".env present in artifact entries: %v", names)
	}
	if !containsName(names, "app/main.py") {
		t.Fatalf("app/main.py missing from artifact entries: %v", names)
	}
}

// TestBuildArtifactDotEnvExclusionSurvivesInclude proves the .env deny is
// actually unconditional (design (b)): an include: entry naming .env
// explicitly must not resurrect it. Without this test, a future change
// that made include: win over every exclusion — not just .gitignore's —
// would pass every other test in this file.
func TestBuildArtifactDotEnvExclusionSurvivesInclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py": "def handler(): pass\n",
		".env":        "NEON_API_KEY=super-secret\n",
	})

	data, _, err := buildArtifact(dir, []string{".env"})
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	if containsName(names, ".env") {
		t.Fatalf(`include: [".env"] resurrected .env in artifact entries: %v`, names)
	}
}

// TestBuildArtifactExcludesDotGitAlways proves the second unconditional
// deny: .git never ships, whether or not the service's .gitignore even
// mentions it. Design (b) calls this out specifically because ".git/"
// never appears in a real .gitignore's own vocabulary — git does not
// gitignore itself — so this exclusion can never lean on rule (2) at all;
// it needs its own unconditional check or nothing would ever apply it.
func TestBuildArtifactExcludesDotGitAlways(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py": "def handler(): pass\n",
		".git/config": "[core]\n",
		".git/HEAD":   "ref: refs/heads/main\n",
	})

	data, _, err := buildArtifact(dir, nil)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	for _, n := range names {
		if n == ".git" || strings.HasPrefix(n, ".git/") {
			t.Fatalf(".git entry present in artifact: %q (all entries: %v)", n, names)
		}
	}
}

// TestBuildArtifactGitignoreExcludesVenvSoSymlinksNeverTripCheck is the
// hard blocker from the brief: a virtualenv's symlinks (.venv/bin/python
// and friends) must not fail the build once .venv/ is gitignored, because
// buildArtifact prunes the whole directory with fs.SkipDir before ever
// looking inside it — see buildArtifact's own doc comment on why pruning,
// not per-file filtering, is what keeps these symlinks from ever reaching
// the symlink check.
func TestBuildArtifactGitignoreExcludesVenvSoSymlinksNeverTripCheck(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py":        "def handler(): pass\n",
		"requirements.txt":   "fastapi==0.115.0\n",
		".gitignore":         ".venv/\n__pycache__/\n",
		".venv/bin/activate": "#!/bin/sh\n",
	})
	if err := os.Symlink("/usr/bin/python3.14", filepath.Join(dir, ".venv", "bin", "python")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	data, _, err := buildArtifact(dir, nil)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	for _, n := range names {
		if strings.HasPrefix(n, ".venv/") {
			t.Fatalf(".venv entry present in artifact: %q (all entries: %v)", n, names)
		}
	}
	if !containsName(names, "app/main.py") || !containsName(names, "requirements.txt") {
		t.Fatalf("expected app/main.py and requirements.txt in artifact entries: %v", names)
	}
}

// TestBuildArtifactIncludeReAddsGitignoredPath proves design (c): a
// service's include: escape hatch re-adds a path its own .gitignore
// excludes. kraai-api's own .gitignore is the real-world case this exists
// for: build/ and requirements.txt are gitignored build output that is
// also exactly what the Lambda zip needs to contain.
func TestBuildArtifactIncludeReAddsGitignoredPath(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py":      "def handler(): pass\n",
		"build/output.txt": "packaged\n",
		"requirements.txt": "fastapi==0.115.0\n",
		".gitignore":       "build/\nrequirements.txt\n*.zip\n",
	})

	data, _, err := buildArtifact(dir, []string{"build/", "requirements.txt"})
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	for _, want := range []string{"app/main.py", "build/output.txt", "requirements.txt"} {
		if !containsName(names, want) {
			t.Fatalf("%q missing from artifact entries: %v", want, names)
		}
	}
}

// TestBuildArtifactWithoutIncludeStillExcludesGitignoredPath is the control
// for TestBuildArtifactIncludeReAddsGitignoredPath: without include:, the
// same .gitignore rule actually does exclude build/ — proving the other
// test's re-inclusion is include: doing the work, not the .gitignore rule
// silently failing to apply at all.
func TestBuildArtifactWithoutIncludeStillExcludesGitignoredPath(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py":      "def handler(): pass\n",
		"build/output.txt": "packaged\n",
		".gitignore":       "build/\n",
	})

	data, _, err := buildArtifact(dir, nil)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	if containsName(names, "build/output.txt") {
		t.Fatalf("build/output.txt present despite gitignore exclusion and no include:: %v", names)
	}
}

// TestBuildArtifactGitignoreNegationHonored proves negation (!) is honoured
// via go-git's real gitignore semantics, not a hand-rolled subset that only
// covers the simple cases this file's other tests exercise.
func TestBuildArtifactGitignoreNegationHonored(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py": "def handler(): pass\n",
		"debug.log":   "noisy\n",
		"keep.log":    "keep me\n",
		".gitignore":  "*.log\n!keep.log\n",
	})

	data, _, err := buildArtifact(dir, nil)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}
	names := zipNames(t, data)
	if containsName(names, "debug.log") {
		t.Fatalf("debug.log present despite *.log exclusion: %v", names)
	}
	if !containsName(names, "keep.log") {
		t.Fatalf("keep.log missing despite !keep.log negation: %v", names)
	}
}

// TestBuildArtifactDeterministicWithExclusionsActive extends the brief's
// own core determinism guarantee (TestBuildArtifactDeterministic) to a
// tree where exclusions are actually doing work — gitignore, the
// unconditional .env deny, and include: all active at once — because
// nothing about walking with pruning and matching should reintroduce the
// host- or order-dependence the unfiltered walk was already proven free of.
func TestBuildArtifactDeterministicWithExclusionsActive(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"app/main.py":        "def handler(): pass\n",
		"build/output.txt":   "packaged\n",
		"requirements.txt":   "fastapi==0.115.0\n",
		".env":               "NEON_API_KEY=super-secret\n",
		".gitignore":         ".venv/\nbuild/\nrequirements.txt\n.env\n",
		".venv/bin/activate": "#!/bin/sh\n",
	})
	if err := os.Symlink("/usr/bin/python3.14", filepath.Join(dir, ".venv", "bin", "python")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	include := []string{"build/", "requirements.txt"}

	data1, sha1, err := buildArtifact(dir, include)
	if err != nil {
		t.Fatalf("buildArtifact (first): %v", err)
	}
	data2, sha2, err := buildArtifact(dir, include)
	if err != nil {
		t.Fatalf("buildArtifact (second): %v", err)
	}
	if sha1 != sha2 {
		t.Fatalf("sha256 differs across builds with exclusions active: %s vs %s", sha1, sha2)
	}
	if !bytes.Equal(data1, data2) {
		t.Fatal("zip bytes differ across builds with exclusions active")
	}

	names := zipNames(t, data1)
	if containsName(names, ".env") {
		t.Fatalf(".env present despite unconditional deny: %v", names)
	}
	for _, n := range names {
		if strings.HasPrefix(n, ".venv/") {
			t.Fatalf(".venv entry present in artifact: %q (all entries: %v)", n, names)
		}
	}
	for _, want := range []string{"app/main.py", "build/output.txt", "requirements.txt"} {
		if !containsName(names, want) {
			t.Fatalf("%q missing from artifact entries: %v", want, names)
		}
	}
}
