package plugin

import (
	"io"
	"os"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

//go:generate go run go.uber.org/mock/mockgen -source=fs.go -destination=mock_fs_test.go -package=plugin

// FS abstracts reading a plugin's compiled WASM bytes off disk (D21: every
// external-system touchpoint sits behind an interface). Its ReadFile
// signature intentionally matches internal/manifest.FS's method of the
// same name: a caller already holding a manifest.FS (rooted and
// symlink-contained via os.Root — see internal/manifest/fs.go) can pass it
// here directly, by Go's structural typing, without adapting it or this
// package importing internal/manifest at all. That keeps plugin-runtime's
// own extension surface (D20) free of a dependency on the manifest
// package's shape, while still reusing its symlink-safety property when a
// caller wires the two together.
type FS interface {
	// ReadFile reads the file at name and returns its contents.
	ReadFile(name string) ([]byte, error)
}

// osFS is FS's standalone implementation, rooted at a directory on disk
// via os.Root for the same symlink-containment reason
// internal/manifest.NewFS documents: os.DirFS/plain os.ReadFile follow a
// symlink inside the root straight through to wherever it points, even
// outside the root, at open time. os.Root's methods refuse to.
type osFS struct {
	root *os.Root
}

// NewOSFS returns an FS rooted at root, a directory on the local
// filesystem. Most callers with an existing internal/manifest.FS should
// pass that instead (see FS's doc comment); this constructor exists for
// callers loading plugins independently of a manifest.
func NewOSFS(root string) (FS, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "opening plugin directory %s", root)
	}
	return osFS{root: r}, nil
}

func (f osFS) ReadFile(name string) ([]byte, error) {
	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(file)
}
