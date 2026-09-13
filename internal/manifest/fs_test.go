package manifest_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evatt-labs/kraai/internal/manifest"
)

func TestNewFS_ReadFile(t *testing.T) {
	fsys := mustNewFS(t, "testdata/fsprobe")
	data, err := fsys.ReadFile("services/alpha.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "services: {}\n" {
		t.Fatalf("got %q", data)
	}
}

func TestNewFS_ReadFile_NotExist(t *testing.T) {
	fsys := mustNewFS(t, "testdata/fsprobe")
	_, err := fsys.ReadFile("does-not-exist.yaml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestNewFS_Glob_IsSorted(t *testing.T) {
	fsys := mustNewFS(t, "testdata/fsprobe")
	got, err := fsys.Glob("services/*.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"services/alpha.yaml", "services/zeta.yaml"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNewFS_Glob_BadPatternIsError(t *testing.T) {
	fsys := mustNewFS(t, "testdata/fsprobe")
	if _, err := fsys.Glob("["); err == nil {
		t.Fatalf("expected an error for a malformed glob pattern")
	}
}

func TestNewFS_Glob_NoMatches(t *testing.T) {
	fsys := mustNewFS(t, "testdata/fsprobe")
	got, err := fsys.Glob("nonexistent/*.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestNewFS_NonexistentRootIsValidationError(t *testing.T) {
	_, err := manifest.NewFS(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatalf("expected an error for a nonexistent manifest root")
	}
}

// --- Security regression: symlinks inside the manifest root must not
// escape it (PR #45 review, second round). os.DirFS follows a symlink
// straight through; os.Root refuses to. These fixtures are built with
// os.Symlink into a t.TempDir(), not committed to the repo, since git
// does track symlinks and a committed one would itself be a minor
// footgun for whoever clones this repo on a platform/config that
// materializes it. ---

// symlinkEscapeRoot builds a manifest root under t.TempDir() containing a
// legitimate file, a symlink escaping outside the root to a canary file,
// and a subdirectory glob target — returning the root path and the
// canary's sentinel content.
func symlinkEscapeRoot(t *testing.T) (root, sentinel string) {
	t.Helper()
	sentinel = "SECRET-CANARY-SYMLINK-ESCAPE-DO-NOT-LEAK"

	outsideDir := t.TempDir()
	canaryPath := filepath.Join(outsideDir, "credentials.txt")
	if err := os.WriteFile(canaryPath, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("writing canary file: %v", err)
	}

	root = t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "legit.txt"), []byte("legit-ok"), 0o600); err != nil {
		t.Fatalf("writing legit file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "services"), 0o700); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	if err := os.Symlink(canaryPath, filepath.Join(root, "services", "evil.yaml")); err != nil {
		t.Fatalf("creating escaping symlink: %v", err)
	}

	return root, sentinel
}

func TestNewFS_SymlinkEscapeIsRejectedOnReadFile(t *testing.T) {
	root, sentinel := symlinkEscapeRoot(t)
	fsys := mustNewFS(t, root)

	data, err := fsys.ReadFile("services/evil.yaml")
	if err == nil {
		t.Fatalf("expected an error reading a symlink that escapes the root, got data: %q", data)
	}
	if len(data) != 0 {
		t.Fatalf("expected no data, got %q", data)
	}
	// Belt and suspenders: even on an unexpected success, the sentinel must
	// never appear.
	if strings.Contains(string(data), sentinel) {
		t.Fatalf("canary sentinel leaked: %q", data)
	}
}

func TestNewFS_SymlinkEscapeListsButCannotBeRead(t *testing.T) {
	root, _ := symlinkEscapeRoot(t)
	fsys := mustNewFS(t, root)

	// Glob enumerates directory entries by name — it doesn't dereference
	// symlinks — so the escaping entry legitimately appears in the match
	// list. The safety property is that reading each match (exactly what
	// loadServices in loader.go does) fails for the escaping one.
	matches, err := fsys.Glob("services/*.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := false
	for _, m := range matches {
		if m == "services/evil.yaml" {
			found = true
		}
	}
	if !found {
		t.Fatalf("matches = %v, want it to include services/evil.yaml (Glob lists, it doesn't dereference)", matches)
	}

	for _, m := range matches {
		data, readErr := fsys.ReadFile(m)
		if m == "services/evil.yaml" {
			if readErr == nil {
				t.Fatalf("expected reading the escaping symlink to fail, got data: %q", data)
			}
			continue
		}
		if readErr != nil {
			t.Fatalf("unexpected error reading legitimate match %q: %v", m, readErr)
		}
	}
}
