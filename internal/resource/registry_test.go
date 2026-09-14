package resource

import (
	"strings"
	"testing"

	"go.uber.org/mock/gomock"
)

func newStub(t *testing.T) *MockResource {
	t.Helper()
	return NewMockResource(gomock.NewController(t))
}

func reg(t *testing.T, provider, typ, capability string, phase Phase) Registration {
	t.Helper()
	return Registration{
		Provider: provider, Type: typ, Capability: capability,
		Phase: phase, Lookup: LookupByName, Resource: newStub(t),
	}
}

func TestRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(reg(t, "cloudflare", "d1_database", "database", PhaseStorage)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Lookup("cloudflare/d1_database")
	if !ok {
		t.Fatal("registered type was not found")
	}
	if got.Capability != "database" || got.Phase != PhaseStorage {
		t.Fatalf("registration = %+v", got)
	}
}

// A duplicate must be an explicit act, not a side effect of load order — a
// plugin shadowing a built-in is real, but it cannot depend on which was
// listed first.
func TestRegisterRejectsDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(reg(t, "cloudflare", "d1_database", "database", PhaseStorage)); err != nil {
		t.Fatal(err)
	}
	err := r.Register(reg(t, "cloudflare", "d1_database", "database", PhaseStorage))
	if err == nil {
		t.Fatal("a second registration silently replaced the first")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("got %v", err)
	}
}

// Validation happens at registration, not at first use, so a malformed entry
// fails while the stack trace still points at whoever wrote it.
func TestRegisterValidates(t *testing.T) {
	valid := reg(t, "cloudflare", "d1_database", "database", PhaseStorage)

	tests := []struct {
		name   string
		mutate func(*Registration)
		want   string
	}{
		{"no provider", func(r *Registration) { r.Provider = "" }, "no Provider"},
		{"no type", func(r *Registration) { r.Type = "" }, "no Type"},
		{"no capability", func(r *Registration) { r.Capability = "" }, "no Capability"},
		{"no resource", func(r *Registration) { r.Resource = nil }, "no Resource"},
		{"bad lookup", func(r *Registration) { r.Lookup = "byVibes" }, "lookup strategy"},
		{"bad phase", func(r *Registration) { r.Phase = Phase(99) }, "phase"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.mutate(&candidate)
			err := NewRegistry().Register(candidate)
			if err == nil {
				t.Fatal("an invalid registration was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

// TestResolveExpandsOneCapabilityToSeveralTypes is Q1's shape: a manifest
// entry names a capability and kraai.yaml names the vendor, and one Postgres
// binding becomes both a database branch and the Hyperdrive config fronting
// it — what the JavaScript did by hand, in a fixed order.
func TestResolveExpandsOneCapabilityToSeveralTypes(t *testing.T) {
	r := NewRegistry()
	// Registered out of phase order on purpose.
	if err := r.Register(Registration{
		Provider: "neon", Type: "hyperdrive", Capability: "postgres",
		Phase: PhaseStorage, Lookup: LookupByAttr, Resource: newStub(t),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Registration{
		Provider: "neon", Type: "branch", Capability: "postgres",
		Phase: PhaseDatabase, Lookup: LookupByAttr, Resource: newStub(t),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := r.Resolve("postgres", "neon")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d registrations, want the capability to expand to both", len(got))
	}
	// Phase order, not registration order: the branch must exist before
	// anything fronts it.
	if got[0].Type != "branch" || got[1].Type != "hyperdrive" {
		t.Fatalf("resolved out of phase order: %s then %s", got[0].Type, got[1].Type)
	}
}

func TestResolveErrorsNameWhatIsAvailable(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(reg(t, "neon", "branch", "postgres", PhaseDatabase)); err != nil {
		t.Fatal(err)
	}

	_, err := r.Resolve("mysql", "neon")
	if err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("got %v, want an error listing the known capabilities", err)
	}

	_, err = r.Resolve("postgres", "planetscale")
	if err == nil || !strings.Contains(err.Error(), "neon") {
		t.Fatalf("got %v, want an error listing the providers for that capability", err)
	}
}

func TestAllIsStablyOrdered(t *testing.T) {
	r := NewRegistry()
	for _, entry := range []Registration{
		reg(t, "cloudflare", "queue", "queues", PhaseStorage),
		reg(t, "wrangler", "worker", "compute", PhaseCompute),
		reg(t, "neon", "branch", "postgres", PhaseDatabase),
		reg(t, "cloudflare", "kv_namespace", "keyvalue", PhaseStorage),
	} {
		if err := r.Register(entry); err != nil {
			t.Fatal(err)
		}
	}

	var keys []string
	for _, entry := range r.All() {
		keys = append(keys, entry.Key())
	}
	want := []string{"neon/branch", "cloudflare/kv_namespace", "cloudflare/queue", "wrangler/worker"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v\nwant %v", keys, want)
	}
}

// The decorator is applied at registration, so a type cannot be added without
// instrumentation by forgetting a wrapper at a call site.
func TestDecoratorWrapsAtRegistration(t *testing.T) {
	var wrapped int
	r := NewRegistry(WithDecorator(func(reg Registration) Resource {
		wrapped++
		return reg.Resource
	}))

	if err := r.Register(reg(t, "cloudflare", "r2_bucket", "objects", PhaseStorage)); err != nil {
		t.Fatal(err)
	}
	if wrapped != 1 {
		t.Fatalf("decorator ran %d times, want once at registration", wrapped)
	}
}

func TestPhasesAreOrdered(t *testing.T) {
	got := Phases()
	if len(got) != 3 || got[0] != PhaseDatabase || got[2] != PhaseCompute {
		t.Fatalf("phases = %v", got)
	}
	for _, p := range got {
		if !p.Valid() || p.String() == "unknown" {
			t.Fatalf("phase %d is not fully declared", p)
		}
	}
	if Phase(99).Valid() || Phase(99).String() != "unknown" {
		t.Fatal("an undeclared phase reported itself as valid")
	}
}

func TestRefKey(t *testing.T) {
	ref := Ref{Provider: "cloudflare", Type: "d1_database", Name: "env-a-api-db"}
	if ref.Key() != "cloudflare/d1_database" {
		t.Fatalf("key = %q", ref.Key())
	}
}

func TestLookupStrategyValid(t *testing.T) {
	for _, s := range []LookupStrategy{LookupByName, LookupByAPI, LookupByAttr, LookupByTag} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	if LookupStrategy("").Valid() || LookupStrategy("byGuess").Valid() {
		t.Error("an unknown strategy reported itself valid")
	}
}

// An empty registry must still produce a usable message rather than naming
// nothing at all.
func TestResolveOnAnEmptyRegistry(t *testing.T) {
	_, err := NewRegistry().Resolve("postgres", "neon")
	if err == nil {
		t.Fatal("resolving against an empty registry succeeded")
	}
	if !strings.Contains(err.Error(), "none registered") {
		t.Fatalf("got %v, want it to say nothing is registered", err)
	}
}

// The "known capabilities" and "providers for it" lists have to read properly
// with more than one entry, which is the only case a single-registration
// registry never exercises.
func TestResolveErrorsListSeveralOptions(t *testing.T) {
	r := NewRegistry()
	for _, entry := range []Registration{
		reg(t, "neon", "branch", "postgres", PhaseDatabase),
		reg(t, "supabase", "branch", "postgres", PhaseDatabase),
		reg(t, "cloudflare", "kv_namespace", "keyvalue", PhaseStorage),
	} {
		if err := r.Register(entry); err != nil {
			t.Fatal(err)
		}
	}

	_, err := r.Resolve("mysql", "planetscale")
	if err == nil {
		t.Fatal("an unknown capability resolved")
	}
	if !strings.Contains(err.Error(), "keyvalue, postgres") {
		t.Fatalf("got %v, want both known capabilities listed", err)
	}

	_, err = r.Resolve("postgres", "planetscale")
	if err == nil {
		t.Fatal("an unknown provider resolved")
	}
	if !strings.Contains(err.Error(), "neon, supabase") {
		t.Fatalf("got %v, want both providers listed", err)
	}
}

// TestVendorSelectsAcrossProviders is D30's real shape: fulfilling one
// capability can take resources from more than one API, and the manifest names
// only the vendor. Keying resolution by provider instead made the second half
// unreachable — a database provisioned with nothing in front of it.
func TestVendorSelectsAcrossProviders(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Registration{
		Provider: "neon", Type: "branch", Capability: "postgres",
		Phase: PhaseDatabase, Lookup: LookupByAttr, Resource: newStub(t),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Registration{
		// Cloudflare's API, Neon's choice.
		Provider: "cloudflare", Type: "hyperdrive", Vendor: "neon",
		Capability: "postgres", Phase: PhaseStorage,
		Lookup: LookupByAttr, Resource: newStub(t),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := r.Resolve("postgres", "neon")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("vendor=neon resolved to %d type(s), want both halves of the choice", len(got))
	}
	if got[0].Type != "branch" || got[1].Type != "hyperdrive" {
		t.Fatalf("resolved out of phase order: %v", got)
	}

	// The companion is not independently selectable by its own provider name.
	if _, err := r.Resolve("postgres", "cloudflare"); err == nil {
		t.Fatal("the companion resolved under its provider rather than its vendor")
	}
}

// Vendor defaults to Provider, which is the common case and must not need
// stating.
func TestVendorDefaultsToProvider(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(reg(t, "cloudflare", "kv_namespace", "keyvalue", PhaseStorage)); err != nil {
		t.Fatal(err)
	}
	got, err := r.Resolve("keyvalue", "cloudflare")
	if err != nil || len(got) != 1 {
		t.Fatalf("Resolve = %v, %v", got, err)
	}
}

// Two vendors competing for one capability stay separate — choosing one must
// never pull in the other's resources.
func TestCompetingVendorsStaySeparate(t *testing.T) {
	r := NewRegistry()
	for _, entry := range []Registration{
		{Provider: "neon", Type: "branch", Capability: "postgres",
			Phase: PhaseDatabase, Lookup: LookupByAttr, Resource: newStub(t)},
		{Provider: "supabase", Type: "branch", Capability: "postgres",
			Phase: PhaseDatabase, Lookup: LookupByAttr, Resource: newStub(t)},
	} {
		if err := r.Register(entry); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.Resolve("postgres", "neon")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Provider != "neon" {
		t.Fatalf("choosing neon pulled in %v", got)
	}
}
