package plugin

import (
	"context"
	"testing"
)

func handleReturning(v string) Handle {
	return HandleFunc(func(context.Context, []byte) ([]byte, error) {
		return []byte(v), nil
	})
}

// TestRegistryOverrideAndRestore is acceptance criterion 3: a plugin
// overriding a built-in wins, with a recorded warning; removing it
// restores the built-in.
func TestRegistryOverrideAndRestore(t *testing.T) {
	ctx := t.Context()
	r := NewRegistry()

	r.Register("provider/cloudflare", BuiltinSource, handleReturning("builtin"))

	handle, ok := r.Lookup("provider/cloudflare")
	if !ok {
		t.Fatal("expected the built-in to be registered")
	}
	out, _ := handle.Invoke(ctx, nil)
	if string(out) != "builtin" {
		t.Fatalf("got %q, want %q", out, "builtin")
	}
	if len(r.Warnings()) != 0 {
		t.Fatalf("expected no warnings yet, got %v", r.Warnings())
	}

	warning, overrode := r.Register("provider/cloudflare", "cost-guard-plugin", handleReturning("plugin"))
	if !overrode {
		t.Fatal("expected overriding the built-in to report an override")
	}
	if warning.Winner != "cost-guard-plugin" || warning.Loser != BuiltinSource || warning.Key != "provider/cloudflare" {
		t.Fatalf("unexpected warning: %+v", warning)
	}
	if got := r.Warnings(); len(got) != 1 || got[0] != warning {
		t.Fatalf("Warnings() = %v, want [%v]", got, warning)
	}

	handle, ok = r.Lookup("provider/cloudflare")
	if !ok {
		t.Fatal("expected a registration to still be present")
	}
	out, _ = handle.Invoke(ctx, nil)
	if string(out) != "plugin" {
		t.Fatalf("got %q, want %q (the plugin should win)", out, "plugin")
	}

	if err := r.Deregister("provider/cloudflare", "cost-guard-plugin"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}

	handle, ok = r.Lookup("provider/cloudflare")
	if !ok {
		t.Fatal("expected the built-in to be restored after removing the plugin")
	}
	out, _ = handle.Invoke(ctx, nil)
	if string(out) != "builtin" {
		t.Fatalf("got %q, want %q (built-in should be restored)", out, "builtin")
	}
}

func TestRegistryDeregisterUnknownSourceErrors(t *testing.T) {
	r := NewRegistry()
	r.Register("k", BuiltinSource, handleReturning("v"))

	if err := r.Deregister("k", "never-registered"); err == nil {
		t.Fatal("expected an error deregistering a source that never registered")
	}
	if err := r.Deregister("no-such-key", "anything"); err == nil {
		t.Fatal("expected an error deregistering an unknown key")
	}
}

func TestRegistryDeregisterLastEntryRemovesKey(t *testing.T) {
	r := NewRegistry()
	r.Register("k", BuiltinSource, handleReturning("v"))

	if err := r.Deregister("k", BuiltinSource); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if _, ok := r.Lookup("k"); ok {
		t.Fatal("expected the key to be gone once its only registration is removed")
	}
}

func TestRegistryLookupMissingKey(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("expected Lookup to report false for an unregistered key")
	}
}

// TestRegistryNoOverrideWarningOnFirstRegistration ensures a plain,
// non-overriding registration never fabricates a warning.
func TestRegistryNoOverrideWarningOnFirstRegistration(t *testing.T) {
	r := NewRegistry()
	_, overrode := r.Register("k", BuiltinSource, handleReturning("v"))
	if overrode {
		t.Fatal("first registration for a key must never report an override")
	}
	if len(r.Warnings()) != 0 {
		t.Fatalf("expected no warnings, got %v", r.Warnings())
	}
}
