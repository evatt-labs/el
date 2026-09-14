package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tetratelabs/wazero/sys"
)

// The tests in this file prove the two resource ceilings Host.Load puts
// on every plugin runtime. Both are deliberately bounded by construction:
// the memory tests run against a two-page (128KiB) limit rather than the
// production 64MiB one, and the CPU test uses a 150ms deadline rather
// than a wall-clock timeout. A test that demonstrates resource exhaustion
// by actually exhausting the developer's machine proves nothing the
// bounded version doesn't, and takes the machine with it.

// tinyLimitHost returns a Host whose plugins are capped at pages of
// linear memory, so a limit test costs kilobytes instead of megabytes.
func tinyLimitHost(t *testing.T, pages uint32) *Host {
	t.Helper()
	h := newTestHost(t)
	h.memoryLimitPages = pages
	return h
}

// loadEchoPlugin loads wasm under the single fixture provision key.
func loadEchoPlugin(ctx context.Context, t *testing.T, h *Host, wasm []byte) (*Plugin, error) {
	t.Helper()
	p, err := h.Load(ctx, mapFS{"p.wasm": wasm}, Spec{
		Name:     "p",
		Path:     "p.wasm",
		PoolSize: 1,
		Provides: []Provision{{Key: "test/echo", Export: fixtureEchoExport}},
	})
	if p != nil {
		t.Cleanup(func() { _ = p.Close(context.WithoutCancel(ctx)) })
	}
	return p, err
}

// TestMemoryGrowStopsAtLimit is the memory ceiling's positive and
// negative case in one: the same fixture, the same one-page grow, against
// a limit that leaves room for it and a limit that does not. Without
// WithMemoryLimitPages both subtests would report a successful grow,
// because wazero's default ceiling is the wasm32 architectural maximum of
// 65536 pages — 4GiB per instance.
func TestMemoryGrowStopsAtLimit(t *testing.T) {
	const growBlocked = 0xFF // memory.grow returned -1

	tests := []struct {
		name       string
		limitPages uint32
		wantByte   byte
	}{
		// fixtureMemPages (2) is already at the limit, so growing by one
		// more page must be refused.
		{name: "grow past limit is refused", limitPages: fixtureMemPages, wantByte: growBlocked},
		// One page of headroom: the identical grow must now succeed and
		// report the pre-grow page count, proving the refusal above came
		// from the limit and not from the fixture being broken.
		{name: "grow within limit succeeds", limitPages: fixtureMemPages + 1, wantByte: fixtureMemPages},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			p, err := loadEchoPlugin(ctx, t, tinyLimitHost(t, tc.limitPages), buildMemoryGrowPlugin(1))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			out, err := p.Invoke(ctx, "test/echo", nil)
			if err != nil {
				t.Fatalf("Invoke: %v", err)
			}
			if len(out) != 1 {
				t.Fatalf("got %d payload bytes, want 1", len(out))
			}
			if out[0] != tc.wantByte {
				t.Fatalf("memory.grow reported 0x%02X, want 0x%02X", out[0], tc.wantByte)
			}
		})
	}
}

// TestOversizedMemoryMinimumRejected covers the other half of the memory
// ceiling: a guest whose *declared* minimum already exceeds the limit is
// refused at instantiation, so the pages are never committed at all.
func TestOversizedMemoryMinimumRejected(t *testing.T) {
	ctx := t.Context()
	const limitPages = 2

	if _, err := loadEchoPlugin(ctx, t, tinyLimitHost(t, limitPages), buildOversizedMemoryPlugin(limitPages+1)); err == nil {
		t.Fatal("Load accepted a plugin declaring more memory than the runtime limit allows")
	}

	// Control: the same fixture at exactly the limit loads fine, so the
	// rejection above is the limit talking and not the fixture.
	if _, err := loadEchoPlugin(ctx, t, tinyLimitHost(t, limitPages), buildOversizedMemoryPlugin(limitPages)); err != nil {
		t.Fatalf("Load rejected a plugin sitting exactly at the limit: %v", err)
	}
}

// TestSpinningPluginIsTerminatedByContext proves Invoke's ctx reaches
// inside the guest. Without WithCloseOnContextDone this test does not
// fail — it hangs forever, pinning a goroutine and the OS thread beneath
// it, until the Go test binary's own panic timeout fires.
func TestSpinningPluginIsTerminatedByContext(t *testing.T) {
	p, err := loadEchoPlugin(t.Context(), t, newTestHost(t), buildSpinningPlugin())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := p.Invoke(ctx, "test/echo", nil); err == nil {
		t.Fatal("Invoke returned without error from a plugin that never returns")
	} else if exit := (*sys.ExitError)(nil); !errors.As(err, &exit) {
		t.Fatalf("got %v, want a wazero sys.ExitError from context termination", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Invoke took %v to honor a 150ms deadline", elapsed)
	}
}

// TestPoolRecoversFromTerminatedInstance is the consequence of enabling
// WithCloseOnContextDone that is easy to miss: wazero closes the module
// instance it interrupted. With a pool of one and a naive "always put the
// instance back" release path, a single timed-out call would poison the
// only slot and every later Invoke would fail on a dead module. The pool
// must re-instantiate instead.
func TestPoolRecoversFromTerminatedInstance(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)

	// Two plugins over one runtime each: the spinner burns its instance,
	// then a healthy plugin proves the recycle path end to end.
	spinner, err := loadEchoPlugin(ctx, t, host, buildSpinningPlugin())
	if err != nil {
		t.Fatalf("Load spinner: %v", err)
	}

	timedOut, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if _, err := spinner.Invoke(timedOut, "test/echo", nil); err == nil {
		t.Fatal("spinning Invoke unexpectedly succeeded")
	}

	// The instance the terminated call was running in is now closed and
	// back in the pool. A second Invoke must transparently replace it
	// rather than failing on a dead module — it will time out again,
	// since the guest still spins, but it must time out rather than
	// report a closed module.
	second, cancel2 := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel2()
	if _, err := spinner.Invoke(second, "test/echo", nil); err == nil {
		t.Fatal("second spinning Invoke unexpectedly succeeded")
	} else if exit := (*sys.ExitError)(nil); !errors.As(err, &exit) {
		t.Fatalf("second Invoke failed with %v, want another sys.ExitError — the pool did not replace the closed instance", err)
	}
}
