package plugin

import (
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	cases := []struct{ ptr, length uint32 }{
		{0, 0},
		{1, 1},
		{0xFFFFFFFF, 0xFFFFFFFF},
		{fixtureOutBase, 128},
	}
	for _, tc := range cases {
		got := pack(tc.ptr, tc.length)
		ptr, length := unpack(got)
		if ptr != tc.ptr || length != tc.length {
			t.Fatalf("pack/unpack(%d, %d) round-tripped as (%d, %d)", tc.ptr, tc.length, ptr, length)
		}
	}
}

// memoryFixture returns a real api.Memory backed by a one-page (64KiB)
// module, for testing readRegion/writeRegion's boundary arithmetic
// directly against wazero's own Memory implementation rather than a
// fake.
func memoryFixture(t *testing.T) api.Memory {
	t.Helper()
	ctx := t.Context()
	m := &wasmModule{}
	m.setMemoryPages(1)
	m.addExportMemory(exportMemory, 0)

	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	compiled, err := rt.CompileModule(ctx, m.bytes())
	if err != nil {
		t.Fatalf("CompileModule: %v", err)
	}
	mod, err := rt.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		t.Fatalf("InstantiateModule: %v", err)
	}
	return mod.Memory()
}

func TestReadRegionBoundary(t *testing.T) {
	mem := memoryFixture(t) // 65536 bytes
	const memSize = 65536

	if _, err := readRegion(mem, 0, 0); err != nil {
		t.Fatalf("zero-length read at 0 should succeed: %v", err)
	}
	if _, err := readRegion(mem, memSize, 0); err != nil {
		t.Fatalf("zero-length read exactly at memory size should succeed: %v", err)
	}
	if _, err := readRegion(mem, memSize-1, 1); err != nil {
		t.Fatalf("1-byte read of the last valid byte should succeed: %v", err)
	}
	if _, err := readRegion(mem, memSize, 1); err == nil {
		t.Fatal("expected an error reading 1 byte starting exactly at memory size")
	}
	if _, err := readRegion(mem, 0, MaxTransferBytes+1); err == nil {
		t.Fatal("expected an error for a length over MaxTransferBytes")
	}
	// ptr near uint32 max with a small length: naive uint32 ptr+len
	// arithmetic wraps to a small number and could pass a buggy bounds
	// check. readRegion must reject it regardless.
	if _, err := readRegion(mem, 0xFFFFFFF0, 0x20); err == nil {
		t.Fatal("expected an error for a ptr+len combination that overflows uint32")
	}
}

func TestWriteRegionBoundary(t *testing.T) {
	mem := memoryFixture(t)
	const memSize = 65536

	if err := writeRegion(mem, 0, nil); err != nil {
		t.Fatalf("writing zero bytes should succeed: %v", err)
	}
	if err := writeRegion(mem, memSize-4, []byte("data")); err != nil {
		t.Fatalf("writing up to exactly memory size should succeed: %v", err)
	}
	if err := writeRegion(mem, memSize-3, []byte("data")); err == nil {
		t.Fatal("expected an error writing 1 byte past memory size")
	}
	if err := writeRegion(mem, 0xFFFFFFF0, []byte("data")); err == nil {
		t.Fatal("expected an error for an out-of-range ptr near uint32 max")
	}
}

func TestDecodeEnvelope(t *testing.T) {
	if _, _, err := decodeEnvelope(nil); err == nil {
		t.Fatal("expected an error decoding an empty envelope")
	}
	status, payload, err := decodeEnvelope([]byte{StatusOK, 'h', 'i'})
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if status != StatusOK || string(payload) != "hi" {
		t.Fatalf("got status=%d payload=%q", status, payload)
	}
}
