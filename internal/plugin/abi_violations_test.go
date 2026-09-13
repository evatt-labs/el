package plugin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tetratelabs/wazero"
)

func TestPluginProvides(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{"echo.wasm": buildValidPlugin()}
	provides := []Provision{{Key: "test/echo", Export: fixtureEchoExport}}
	p, err := host.Load(ctx, fsys, Spec{Name: "echo", Path: "echo.wasm", Provides: provides})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })

	if got := p.Provides(); len(got) != 1 || got[0] != provides[0] {
		t.Fatalf("Provides() = %v, want %v", got, provides)
	}
}

func TestWarningString(t *testing.T) {
	w := Warning{Key: "k", Winner: "plugin-a", Loser: BuiltinSource}
	got := w.String()
	if got == "" {
		t.Fatal("expected a non-empty warning message")
	}
	t.Logf("warning message: %s", got)
}

func TestNewHostRejectsUnusableCacheDir(t *testing.T) {
	dir := t.TempDir()
	// A regular file where NewHost expects to create/use a directory.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing blocker file: %v", err)
	}

	if _, err := NewHost(filepath.Join(blocker, "cache")); err == nil {
		t.Fatal("expected NewHost to reject a cache dir path blocked by a regular file")
	}
}

func TestLoadRejectsUnreadablePath(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{} // "missing.wasm" not present

	if _, err := host.Load(ctx, fsys, Spec{Name: "p", Path: "missing.wasm"}); err == nil {
		t.Fatal("expected an error for a plugin path the FS can't read")
	}
}

func TestLoadRejectsInvalidWasmBytes(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{"garbage.wasm": []byte("this is not a wasm module")}

	_, err := host.Load(ctx, fsys, Spec{Name: "p", Path: "garbage.wasm"})
	if err == nil {
		t.Fatal("expected an error compiling invalid WASM bytes")
	}
	assertValidation(t, err)
}

// buildWrongSignatureAlloc returns an otherwise-valid module whose
// kraai_alloc export has the right name but the wrong signature
// ((i32,i32) -> i32 instead of (i32) -> i32) — exercises
// matchesSignature's mismatch branch distinctly from a missing export.
func buildWrongSignatureAlloc() []byte {
	m := &wasmModule{}
	tNoneI32 := m.addType(nil, []byte{valI32})
	tI32I32ToI32 := m.addType([]byte{valI32, valI32}, []byte{valI32}) // wrong: alloc takes 1 param, not 2
	tI32I32ToNone := m.addType([]byte{valI32, valI32}, nil)

	version := m.addFunc(tNoneI32, funcBody(goodVersionBody()))
	alloc := m.addFunc(tI32I32ToI32, funcBody(iI32Const(fixtureAllocBase)))
	dealloc := m.addFunc(tI32I32ToNone, funcBody(nil))

	m.addExportFunc(funcABIVersion, version)
	m.addExportFunc(funcAlloc, alloc)
	m.addExportFunc(funcDealloc, dealloc)
	m.setMemoryPages(1)
	m.addExportMemory(exportMemory, 0)
	return m.bytes()
}

func TestLoadRejectsWrongSignatureExport(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{"bad.wasm": buildWrongSignatureAlloc()}

	_, err := host.Load(ctx, fsys, Spec{Name: "bad", Path: "bad.wasm"})
	if err == nil {
		t.Fatal("expected an error for a wrong-signature kraai_alloc export")
	}
	assertValidation(t, err)
}

// buildLyingHostCallPlugin is like buildHostCallPlugin, but its provision
// ignores its real (ptr, len) input entirely and calls the granted host
// capability with a hardcoded, out-of-range request region instead —
// simulating a guest that lies about its own memory when asking the host
// to read a request from it.
func buildLyingHostCallPlugin(capName string) []byte {
	m := &wasmModule{}
	tI32I32ToI64 := m.addType([]byte{valI32, valI32}, []byte{valI64})
	imported := m.addImportFunc(HostNamespace, capName, tI32I32ToI64)

	addStandardTriad(m, goodVersionBody(), goodAllocBody(), nil)

	body := concatBytes(iI32Const(-16) /* 0xFFFFFFF0 */, iI32Const(4), iCall(imported))
	call := m.addFunc(tI32I32ToI64, funcBody(body))
	m.addExportFunc(fixtureHostCallExport, call)

	m.setMemoryPages(fixtureMemPages)
	m.addExportMemory(exportMemory, 0)
	return m.bytes()
}

func TestHostCapabilityRejectsGuestLyingAboutOwnRequest(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t, stubCapability{
		name: testCapabilityName,
		fn:   func(_ context.Context, input []byte) ([]byte, error) { return input, nil },
	})
	fsys := mapFS{"lying.wasm": buildLyingHostCallPlugin(testCapabilityName)}

	p, err := host.Load(ctx, fsys, Spec{
		Name:     "lying",
		Path:     "lying.wasm",
		Grants:   []string{testCapabilityName},
		Provides: []Provision{{Key: "test/call", Export: fixtureHostCallExport}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })

	if _, err := p.Invoke(ctx, "test/call", []byte("x")); err == nil {
		t.Fatal("expected Invoke to fail when the guest lies about its own request region")
	}
}

// TestPlaceInGuestMemoryMissingAlloc unit-tests placeInGuestMemory's
// defensive nil-alloc branch directly: a module missing kraai_alloc
// entirely can only be produced deliberately (Plugin.validateABI already
// refuses to load one), so this bypasses Plugin/Host and instantiates the
// fixture directly to exercise the check itself.
func TestPlaceInGuestMemoryMissingAlloc(t *testing.T) {
	ctx := t.Context()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })

	compiled, err := rt.CompileModule(ctx, buildMissingExport("alloc"))
	if err != nil {
		t.Fatalf("CompileModule: %v", err)
	}
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		t.Fatalf("InstantiateModule: %v", err)
	}

	if _, err := placeInGuestMemory(ctx, mod, []byte("x")); err == nil {
		t.Fatal("expected an error when the module has no kraai_alloc export")
	}
}
