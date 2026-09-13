package plugin

import (
	"context"
	"testing"
)

// TestPoolGetHonorsContextCancellation exercises get's ctx.Done() branch:
// a pool with no free instance and a canceled context must return the
// context's error rather than blocking forever.
func TestPoolGetHonorsContextCancellation(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{"echo.wasm": buildValidPlugin()}
	p, err := host.Load(ctx, fsys, Spec{Name: "echo", Path: "echo.wasm", PoolSize: 1})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })

	// Drain the only instance so a further get() has nothing to return.
	mod, err := p.pool.get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	t.Cleanup(func() { p.pool.put(mod) })

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.pool.get(cancelCtx); err == nil {
		t.Fatal("expected get to fail on an already-canceled context")
	}
}

// buildStatusByteCapability returns a valid-triad module whose capability
// writes an arbitrary status byte (neither StatusOK nor StatusError) —
// exercising callExport's "unknown status byte" branch, a distinct
// failure mode from a bad (ptr, len) or a reported StatusError.
func buildStatusByteCapability(status byte) []byte {
	m := &wasmModule{}
	addStandardTriad(m, goodVersionBody(), goodAllocBody(), nil)

	tI32I32ToI64 := m.addType([]byte{valI32, valI32}, []byte{valI64})
	body := concatBytes(
		iI32Const(fixtureOutBase), iI32Const(int32(status)), iI32Store8(),
		packConst(fixtureOutBase, 1),
	)
	fn := m.addFunc(tI32I32ToI64, funcBody(body))
	m.addExportFunc(fixtureEchoExport, fn)

	m.setMemoryPages(fixtureMemPages)
	m.addExportMemory(exportMemory, 0)
	return m.bytes()
}

func TestInvokeRejectsUnknownStatusByte(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{"bad.wasm": buildStatusByteCapability(7)}

	p, err := host.Load(ctx, fsys, Spec{
		Name:     "bad",
		Path:     "bad.wasm",
		Provides: []Provision{{Key: "test/bad", Export: fixtureEchoExport}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })

	_, err = p.Invoke(ctx, "test/bad", []byte("x"))
	if err == nil {
		t.Fatal("expected an error for an unrecognized status byte")
	}
	assertValidation(t, err)
}

func TestValueTypesEqualLengthMismatch(t *testing.T) {
	if valueTypesEqual(nil, []byte{valI32}) {
		t.Fatal("expected a length mismatch (0 vs 1) to compare unequal")
	}
	if valueTypesEqual([]byte{valI32}, nil) {
		t.Fatal("expected a length mismatch (1 vs 0) to compare unequal")
	}
}
