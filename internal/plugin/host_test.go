package plugin

import (
	"context"
	"testing"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// stubCapability is a minimal Capability test double: it doesn't touch
// the network or filesystem, just records what it was called with and
// returns a canned response.
type stubCapability struct {
	name string
	fn   func(ctx context.Context, input []byte) ([]byte, error)
}

func (c stubCapability) Name() string { return c.name }
func (c stubCapability) Invoke(ctx context.Context, input []byte) ([]byte, error) {
	return c.fn(ctx, input)
}

const testCapabilityName = "test_capability"

// TestUnwiredCapabilityRejected is acceptance criterion 2: a plugin
// attempting an unwired capability (here, importing a host function this
// Host never provides at all — the same failure a plugin trying raw
// filesystem access outside its grants would hit) fails, provably, at
// load time, before any guest code runs.
func TestUnwiredCapabilityRejected(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t) // no capabilities registered at all
	fsys := mapFS{"bad.wasm": buildUnwiredImport(testCapabilityName)}

	_, err := host.Load(ctx, fsys, Spec{Name: "bad", Path: "bad.wasm"})
	if err == nil {
		t.Fatal("expected Load to fail for an unwired import, got nil error")
	}
}

// TestGrantScopedToRequestingPlugin proves a capability is only reachable
// by a plugin whose spec names it in Grants — registering it with Host
// isn't enough on its own, and one plugin's grant never leaks to another
// plugin in the same Host that didn't ask for it.
func TestGrantScopedToRequestingPlugin(t *testing.T) {
	ctx := t.Context()
	var called []byte
	capa := stubCapability{
		name: testCapabilityName,
		fn: func(_ context.Context, input []byte) ([]byte, error) {
			called = input
			return []byte("granted-response"), nil
		},
	}
	host := newTestHost(t, capa)

	// The granted plugin: its spec names testCapabilityName, so the
	// import resolves and the round trip works end to end.
	grantedFS := mapFS{"granted.wasm": buildHostCallPlugin(testCapabilityName)}
	granted, err := host.Load(ctx, grantedFS, Spec{
		Name:     "granted",
		Path:     "granted.wasm",
		Grants:   []string{testCapabilityName},
		Provides: []Provision{{Key: "test/call", Export: fixtureHostCallExport}},
	})
	if err != nil {
		t.Fatalf("Load(granted): %v", err)
	}
	t.Cleanup(func() { _ = granted.Close(ctx) })

	out, err := granted.Invoke(ctx, "test/call", []byte("payload"))
	if err != nil {
		t.Fatalf("Invoke(granted): %v", err)
	}
	if string(out) != "granted-response" {
		t.Fatalf("got %q, want %q", out, "granted-response")
	}
	if string(called) != "payload" {
		t.Fatalf("capability saw input %q, want %q", called, "payload")
	}

	// The ungranted plugin: identical module, but its spec never names
	// testCapabilityName in Grants, so its import of the exact same host
	// function name is never wired for it and Load fails.
	ungrantedFS := mapFS{"ungranted.wasm": buildHostCallPlugin(testCapabilityName)}
	_, err = host.Load(ctx, ungrantedFS, Spec{
		Name:     "ungranted",
		Path:     "ungranted.wasm",
		Provides: []Provision{{Key: "test/call", Export: fixtureHostCallExport}},
	})
	if err == nil {
		t.Fatal("expected Load to fail for a plugin that didn't grant the capability it imports")
	}
}

func TestGrantUnknownCapabilityRejected(t *testing.T) {
	ctx := t.Context()
	host := newTestHost(t) // no capabilities registered
	fsys := mapFS{"p.wasm": buildValidPlugin()}

	_, err := host.Load(ctx, fsys, Spec{
		Name:   "p",
		Path:   "p.wasm",
		Grants: []string{"nonexistent_capability"},
	})
	if err == nil {
		t.Fatal("expected Load to reject a grant for a capability the Host never registered")
	}
	assertValidation(t, err)
}

// TestCapabilityInvokeErrorSurfacesAsEnvelope proves a Capability's own
// error return doesn't panic or trap the guest — it's delivered through
// the normal StatusError envelope, and Invoke reports it as an ordinary
// error.
func TestCapabilityInvokeErrorSurfacesAsEnvelope(t *testing.T) {
	ctx := t.Context()
	failing := stubCapability{
		name: testCapabilityName,
		fn: func(context.Context, []byte) ([]byte, error) {
			return nil, kerrors.Validation("capability deliberately failed")
		},
	}
	host := newTestHost(t, failing)
	fsys := mapFS{"p.wasm": buildHostCallPlugin(testCapabilityName)}

	p, err := host.Load(ctx, fsys, Spec{
		Name:     "p",
		Path:     "p.wasm",
		Grants:   []string{testCapabilityName},
		Provides: []Provision{{Key: "test/call", Export: fixtureHostCallExport}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(ctx) })

	_, err = p.Invoke(ctx, "test/call", []byte("x"))
	if err == nil {
		t.Fatal("expected Invoke to surface the capability's error")
	}
}
