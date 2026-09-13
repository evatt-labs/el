package plugin

import "testing"

// mapFS is a trivial in-memory FS for tests: no disk, no symlinks, just a
// fixed map of names to bytes.
type mapFS map[string][]byte

func (m mapFS) ReadFile(name string) ([]byte, error) {
	b, ok := m[name]
	if !ok {
		return nil, &fsNotFoundError{name: name}
	}
	return b, nil
}

type fsNotFoundError struct{ name string }

func (e *fsNotFoundError) Error() string { return "file not found: " + e.name }

// newTestHost builds a Host backed by a fresh on-disk compilation cache
// under t.TempDir(), registering capabilities, and registers its cleanup.
func newTestHost(t *testing.T, capabilities ...Capability) *Host {
	t.Helper()
	h, err := NewHost(t.TempDir(), capabilities...)
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = h.Close(t.Context()) })
	return h
}
