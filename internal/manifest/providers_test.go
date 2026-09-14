package manifest

import (
	"strings"
	"testing"
)

// TestProvidersFor pins the capability lookup the planner uses, so it and the
// resource registry agree on capability names rather than keeping two copies
// that can drift.
func TestProvidersFor(t *testing.T) {
	p := Providers{
		Compute:  &Provider{Vendor: "aws", Settings: map[string]any{"region": "us-east-1"}},
		Database: &Provider{Vendor: "neon"},
	}

	compute, ok := p.For(CapabilityCompute)
	if !ok || compute.Vendor != "aws" {
		t.Fatalf("For(compute) = %+v, %v", compute, ok)
	}
	if compute.Settings["region"] != "us-east-1" {
		t.Fatalf("settings = %+v", compute.Settings)
	}

	// Unconfigured and unknown both report absent, and neither panics.
	if _, ok := p.For(CapabilityObjects); ok {
		t.Error("an unconfigured capability resolved")
	}
	if _, ok := p.For("nonsense"); ok {
		t.Error("an unknown capability resolved")
	}

	if got := p.Capabilities(); len(got) != 2 || got[0] != CapabilityCompute || got[1] != CapabilityDatabase {
		t.Fatalf("Capabilities() = %v", got)
	}
	if got := (Providers{}).Capabilities(); len(got) != 0 {
		t.Fatalf("an empty Providers reported %v", got)
	}
}

// A capability naming no vendor cannot resolve to anything, and the error
// should name the file and key rather than surfacing later as an
// unresolvable registry lookup with no obvious source.
func TestValidateRootRequiresAVendor(t *testing.T) {
	err := validateRoot(&Root{Version: 1, Providers: Providers{Database: &Provider{}}})
	if err == nil {
		t.Fatal("a capability with no vendor was accepted")
	}
	for _, want := range []string{"providers.database", "vendor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}

	if err := validateRoot(&Root{Version: 1, Providers: Providers{Database: &Provider{Vendor: "neon"}}}); err != nil {
		t.Fatalf("a configured vendor was rejected: %v", err)
	}
}

// Settings is free-form by design (the same exemption Values carries), so a
// nested structure has to survive decoding untouched.
func TestProviderSettingsCarryNestedValues(t *testing.T) {
	var root Root
	err := DecodeStrict([]byte(`
version: 1
providers:
  compute:
    vendor: aws
    settings:
      region: us-east-1
      lambda:
        runtime: python3.13
        memoryMb: 512
      tags:
        - one
        - two
`), "kraai.yaml", &root)
	if err != nil {
		t.Fatalf("DecodeStrict: %v", err)
	}

	settings := root.Providers.Compute.Settings
	if settings["region"] != "us-east-1" {
		t.Fatalf("region = %v", settings["region"])
	}
	lambda, ok := settings["lambda"].(map[string]any)
	if !ok {
		t.Fatalf("lambda = %T, want a nested mapping", settings["lambda"])
	}
	if lambda["runtime"] != "python3.13" || lambda["memoryMb"] != 512 {
		t.Fatalf("lambda = %+v", lambda)
	}
	tags, ok := settings["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("tags = %+v", settings["tags"])
	}
}

// Unknown keys are still rejected everywhere except inside settings — that
// exemption must not leak upward into the provider block itself.
func TestProviderRejectsUnknownKeys(t *testing.T) {
	err := DecodeStrict([]byte(`
version: 1
providers:
  compute:
    vendor: aws
    regionn: us-east-1
`), "kraai.yaml", &Root{})
	if err == nil {
		t.Fatal("a misspelled key inside a provider block was accepted")
	}
	if !strings.Contains(err.Error(), "regionn") {
		t.Fatalf("error should name the unknown key: %v", err)
	}
}

// TestDatabaseCapabilityIsEngineAgnostic pins why the capability is
// "database" rather than an engine name. A service's databases: entry already
// carries an engine; naming one in the capability encoded the same fact twice
// and left every other engine with no capability at all — Cloudflare D1
// registered under "database" and was unreachable, because the only database
// capability the manifest offered was "postgres".
func TestDatabaseCapabilityIsEngineAgnostic(t *testing.T) {
	for _, vendor := range []string{"neon", "cloudflare"} {
		p := Providers{Database: &Provider{Vendor: vendor}}
		got, ok := p.For(CapabilityDatabase)
		if !ok || got.Vendor != vendor {
			t.Fatalf("vendor %q did not resolve through the database capability", vendor)
		}
	}
	// The engine name is not a capability.
	if _, ok := (Providers{Database: &Provider{Vendor: "neon"}}).For("postgres"); ok {
		t.Fatal("an engine name resolved as a capability")
	}
}

// TestProvidersVendors covers the map a resource registry resolves against.
// A registration can condition on a capability other than its own, so
// answering "does this apply" needs the whole set rather than one entry.
func TestProvidersVendors(t *testing.T) {
	p := Providers{
		Compute:  &Provider{Vendor: "aws"},
		Database: &Provider{Vendor: "neon"},
	}
	got := p.Vendors()
	if len(got) != 2 || got[CapabilityCompute] != "aws" || got[CapabilityDatabase] != "neon" {
		t.Fatalf("Vendors() = %v", got)
	}
	// An unconfigured capability is absent, not empty-string present: a
	// condition asking for it must read "not configured", not "configured as
	// nothing".
	if _, present := got[CapabilityObjects]; present {
		t.Fatalf("an unconfigured capability appeared in %v", got)
	}
	if got := (Providers{}).Vendors(); len(got) != 0 {
		t.Fatalf("an empty Providers produced %v", got)
	}
}
