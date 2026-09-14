package plugin

import (
	"testing"
	"time"
)

// TestWarmCallOverheadBudget is the first half of acceptance criterion 4:
// a hard regression guard asserting warm per-call overhead stays small,
// per D29's measured 56ns warm-call figure. It calls a trivial, argument-
// free exported function directly (fixtureNoopExport) rather than going
// through Plugin.Invoke's full alloc/write/call/read/dealloc sequence —
// that sequence is itself several wazero calls plus host-side bounds
// checking and is expected to cost more (BenchmarkInvokeRoundTrip below
// reports its real number); this test isolates the one thing D29 and
// docs/workstreams.yaml's acceptance criterion actually describe:
// wazero's own per-call dispatch overhead once a module is compiled and
// instantiated.
//
// The threshold is a regression guard, not a reproduction of the
// appendix's exact 56ns: CI hardware varies enough that asserting "tens
// of nanoseconds" precisely would be flaky. 5 microseconds is roughly two
// orders of magnitude above the measured figure — comfortably wide enough
// to absorb noise, while still failing hard if warm call overhead
// regressed to the microsecond range this package should never see.
func TestWarmCallOverheadBudget(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)
	fsys := mapFS{"echo.wasm": buildValidPlugin()}

	p, err := host.Load(ctx, fsys, Spec{Name: "echo", Path: "echo.wasm"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })

	mod, err := p.pool.get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer p.pool.put(mod)
	fn := mod.ExportedFunction(fixtureNoopExport)
	if fn == nil {
		t.Fatal("fixture does not export kraai_bench_noop")
	}

	const warmup = 1_000
	for i := 0; i < warmup; i++ {
		if _, err := fn.Call(ctx); err != nil {
			t.Fatalf("warmup call: %v", err)
		}
	}

	const iterations = 20_000
	start := time.Now()
	for i := 0; i < iterations; i++ {
		if _, err := fn.Call(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	elapsed := time.Since(start)
	perCall := elapsed / time.Duration(iterations)
	t.Logf("warm call overhead: %s/call over %d iterations", perCall, iterations)

	const budget = 5 * time.Microsecond
	if perCall > budget {
		t.Fatalf("warm call overhead %s/call exceeds the %s regression budget", perCall, budget)
	}
}

// BenchmarkNoopCall reports raw wazero per-call dispatch overhead for
// humans comparing against docs/BLUEPRINT.md's measured 56ns figure —
// informational, not an assertion (see TestWarmCallOverheadBudget for
// the hard regression guard).
func BenchmarkNoopCall(b *testing.B) {
	ctx := b.Context()
	host, err := NewHost(b.TempDir())
	if err != nil {
		b.Fatalf("NewHost: %v", err)
	}
	defer func() { _ = host.Close(ctx) }()

	fsys := mapFS{"echo.wasm": buildValidPlugin()}
	p, err := host.Load(ctx, fsys, Spec{Name: "echo", Path: "echo.wasm"})
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(ctx) }()

	mod, err := p.pool.get(ctx)
	if err != nil {
		b.Fatalf("get: %v", err)
	}
	defer p.pool.put(mod)
	fn := mod.ExportedFunction(fixtureNoopExport)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fn.Call(ctx); err != nil {
			b.Fatalf("Call: %v", err)
		}
	}
}

// BenchmarkInvokeRoundTrip reports Plugin.Invoke's full per-call cost —
// alloc, write, call, read, dealloc, plus this package's own bounds
// checking — against a 16KB payload, the size docs/BLUEPRINT.md's
// measurement appendix pools instances against (19.2us at pool size 1
// down to 4.3us at pool size 16). Informational, alongside
// TestWarmCallOverheadBudget's hard assertion on the narrower,
// argument-free call path.
func BenchmarkInvokeRoundTrip(b *testing.B) {
	ctx := b.Context()
	host, err := NewHost(b.TempDir())
	if err != nil {
		b.Fatalf("NewHost: %v", err)
	}
	defer func() { _ = host.Close(ctx) }()

	fsys := mapFS{"echo.wasm": buildValidPlugin()}
	p, err := host.Load(ctx, fsys, Spec{
		Name:     "echo",
		Path:     "echo.wasm",
		Provides: []Provision{{Key: "bench/echo", Export: fixtureEchoExport}},
		PoolSize: 1,
	})
	if err != nil {
		b.Fatalf("Load: %v", err)
	}
	defer func() { _ = p.Close(ctx) }()

	payload := make([]byte, 16*1024)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := p.Invoke(ctx, "bench/echo", payload); err != nil {
			b.Fatalf("Invoke: %v", err)
		}
	}
}
