package plugin

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

// countingCapability records how many times a guest reached it, which is
// what the re-entry tests actually assert on.
type countingCapability struct {
	name  string
	calls atomic.Int32
}

func (c *countingCapability) Name() string { return c.name }

func (c *countingCapability) Invoke(_ context.Context, _ []byte) ([]byte, error) {
	c.calls.Add(1)
	return []byte("ok"), nil
}

func loadReentrantPlugin(ctx context.Context, t *testing.T, capa Capability, wasm []byte) (*Plugin, error) {
	t.Helper()
	host := newTestHost(t, capa)
	p, err := host.Load(ctx, mapFS{"p.wasm": wasm}, Spec{
		Name:     "p",
		Path:     "p.wasm",
		PoolSize: 1,
		Grants:   []string{capa.Name()},
		Provides: []Provision{{Key: "test/call", Export: fixtureHostCallExport}},
	})
	if p != nil {
		t.Cleanup(func() { _ = p.Close(context.WithoutCancel(ctx)) })
	}
	return p, err
}

// TestUnboundedHostReentryIsRefused is the regression test for the
// package's sharpest failure mode. Delivering a capability's response
// requires calling the guest's own kraai_alloc — the host may not write
// into memory it did not ask the guest to allocate — so a guest whose
// kraai_alloc calls a host capability closes a cycle that adds Go stack
// frames on the caller's goroutine every lap, with no WASM memory limit
// governing the stack it grows.
//
// Before MaxHostCallDepth this did not fail, it aborted: measured against
// a deliberately-lowered 8MiB stack it produced `fatal error: stack
// overflow`, exit status 2. Go's goroutine stack limit is a fatal error
// rather than a panic, so recover() cannot catch it and the whole kraai
// process dies — reachable by any granted plugin, with no memory, no
// syscall, and no exploit beyond an allocator that calls back.
func TestUnboundedHostReentryIsRefused(t *testing.T) {
	ctx := t.Context()
	capa := &countingCapability{name: "test/counter"}

	p, err := loadReentrantPlugin(ctx, t, capa, buildUnboundedReentrantAllocPlugin(capa.Name()))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	_, err = p.Invoke(ctx, "test/call", []byte("x"))
	if err == nil {
		t.Fatal("Invoke succeeded against a plugin whose allocator re-enters the host without bound")
	}
	if !strings.Contains(err.Error(), "re-entry depth") {
		t.Fatalf("got %v, want an error naming the re-entry depth limit", err)
	}
	// The bound must be the host's, not the guest's good behavior: the
	// guest would have recursed forever, so the call count is capped at
	// the limit rather than growing with it.
	if got := capa.calls.Load(); got > MaxHostCallDepth {
		t.Fatalf("capability ran %d times, want at most MaxHostCallDepth (%d)", got, MaxHostCallDepth)
	}
}

// TestShallowHostReentryStillWorks is the control: the depth limit must
// cut only the pathological case. A guest whose allocator re-enters the
// host a few times is odd but within contract, and still completes.
func TestShallowHostReentryStillWorks(t *testing.T) {
	ctx := t.Context()
	capa := &countingCapability{name: "test/counter"}

	p, err := loadReentrantPlugin(ctx, t, capa, buildReentrantAllocPlugin(capa.Name(), 3))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	out, err := p.Invoke(ctx, "test/call", []byte("x"))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(out) != "ok" {
		t.Fatalf("got %q, want %q", out, "ok")
	}
	if got := capa.calls.Load(); got < 2 {
		t.Fatalf("capability ran %d times, want the fixture's several re-entries — the test is no longer exercising re-entry at all", got)
	}
}

// TestOversizedPluginRejected covers the one part of loading a plugin
// that happens entirely outside the sandbox: the .wasm file is read whole
// into host memory and compiled at a cost linear in its size, both before
// any guest instruction runs.
func TestOversizedPluginRejected(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t)

	oversized := make([]byte, MaxPluginBytes+1)
	_, err := host.Load(ctx, mapFS{"big.wasm": oversized}, Spec{
		Name:     "big",
		Path:     "big.wasm",
		Provides: []Provision{{Key: "k", Export: fixtureEchoExport}},
	})
	if err == nil {
		t.Fatal("Load accepted a plugin binary larger than MaxPluginBytes")
	}
	if !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("got %v, want a size-limit error", err)
	}
}
