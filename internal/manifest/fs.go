package manifest

import (
	"io/fs"
	"os"
	"sort"
)

//go:generate go run go.uber.org/mock/mockgen -source=fs.go -destination=mock_fs_test.go -package=manifest

// FS abstracts the filesystem operations the loader needs (D21: every
// external-system touchpoint sits behind an interface). It reads relative
// to a fixed manifest root, mirroring io/fs.FS's rooted semantics, so
// tests can inject an in-memory or failing implementation without ever
// touching disk. Glob results are always returned sorted, so callers get
// deterministic merge order regardless of the underlying filesystem's
// directory-entry order.
type FS interface {
	// ReadFile reads the file at name, a slash-separated path relative to
	// the manifest root.
	ReadFile(name string) ([]byte, error)
	// Glob returns every name relative to the manifest root matching
	// pattern (io/fs.Glob syntax), sorted lexically.
	Glob(pattern string) ([]string, error)
}

// dirFS is FS's real implementation, rooted at a directory on disk via
// os.DirFS.
type dirFS struct {
	fsys fs.FS
}

// NewFS returns an FS rooted at root, a directory on the local filesystem.
func NewFS(root string) FS {
	return dirFS{fsys: os.DirFS(root)}
}

func (d dirFS) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(d.fsys, name)
}

func (d dirFS) Glob(pattern string) ([]string, error) {
	matches, err := fs.Glob(d.fsys, pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}
