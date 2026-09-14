package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOSFSReadFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.wasm"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	fsys, err := NewOSFS(dir)
	if err != nil {
		t.Fatalf("NewOSFS: %v", err)
	}
	out, err := fsys.ReadFile("p.wasm")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("got %q, want %q", out, "hello")
	}
}

func TestOSFSReadFileMissing(t *testing.T) {
	fsys, err := NewOSFS(t.TempDir())
	if err != nil {
		t.Fatalf("NewOSFS: %v", err)
	}
	if _, err := fsys.ReadFile("does-not-exist.wasm"); err == nil {
		t.Fatal("expected an error reading a missing file")
	}
}

func TestNewOSFSRejectsMissingRoot(t *testing.T) {
	if _, err := NewOSFS(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a nonexistent root directory")
	}
}
