package manifest_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/evatt-labs/kraai/internal/manifest"
)

func TestNewFS_ReadFile(t *testing.T) {
	fsys := manifest.NewFS("testdata/fsprobe")
	data, err := fsys.ReadFile("services/alpha.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "services: {}\n" {
		t.Fatalf("got %q", data)
	}
}

func TestNewFS_ReadFile_NotExist(t *testing.T) {
	fsys := manifest.NewFS("testdata/fsprobe")
	_, err := fsys.ReadFile("does-not-exist.yaml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestNewFS_Glob_IsSorted(t *testing.T) {
	fsys := manifest.NewFS("testdata/fsprobe")
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
	fsys := manifest.NewFS("testdata/fsprobe")
	if _, err := fsys.Glob("["); err == nil {
		t.Fatalf("expected an error for a malformed glob pattern")
	}
}

func TestNewFS_Glob_NoMatches(t *testing.T) {
	fsys := manifest.NewFS("testdata/fsprobe")
	got, err := fsys.Glob("nonexistent/*.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}
