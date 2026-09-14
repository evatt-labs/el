package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/manifest"
	"github.com/evatt-labs/kraai/internal/naming"
	"github.com/evatt-labs/kraai/internal/resource"
)

const envName = "swift-otter-badger-10203"

func findAction(t *testing.T, p *Plan, provider, typ string) Action {
	t.Helper()
	for _, a := range p.Actions {
		if a.Provider == provider && a.Type == typ {
			return a
		}
	}
	t.Fatalf("no action for %s/%s in %+v", provider, typ, p.Actions)
	return Action{}
}

// TestPlan_CapabilityExpandsToMultipleTypes pins D30: one Postgres binding,
// with only "neon" configured as its vendor, must still produce both the
// Neon branch and the Cloudflare Hyperdrive configuration fronting it —
// the exact cross-provider case registrationsFor exists for.
func TestPlan_CapabilityExpandsToMultipleTypes(t *testing.T) {
	f := newRegistryFixture(t)
	m := f.oneServiceManifest()

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	branch := findAction(t, p, "neon", "branch")
	hyper := findAction(t, p, "cloudflare", "hyperdrive")

	if branch.Capability != manifest.CapabilityPostgres || hyper.Capability != manifest.CapabilityPostgres {
		t.Fatalf("both expanded types should carry the postgres capability: branch=%q hyper=%q",
			branch.Capability, hyper.Capability)
	}
	if branch.Phase != resource.PhaseDatabase || hyper.Phase != resource.PhaseStorage {
		t.Fatalf("branch/hyperdrive phases = %v/%v, want database/storage", branch.Phase, hyper.Phase)
	}
	// Phase ordering (D31): the branch must be planned before whatever
	// fronts it.
	branchIdx, hyperIdx := -1, -1
	for i, a := range p.Actions {
		if a.Provider == "neon" && a.Type == "branch" {
			branchIdx = i
		}
		if a.Provider == "cloudflare" && a.Type == "hyperdrive" {
			hyperIdx = i
		}
	}
	if branchIdx == -1 || hyperIdx == -1 || branchIdx > hyperIdx {
		t.Fatalf("expected branch before hyperdrive in Actions, got indices %d, %d", branchIdx, hyperIdx)
	}
}

// TestPlan_AbsentResourcesPlanAsCreate covers every kind of declared
// binding planning as Create when Get finds nothing.
func TestPlan_AbsentResourcesPlanAsCreate(t *testing.T) {
	f := newRegistryFixture(t)
	m := f.oneServiceManifest()

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 5 {
		t.Fatalf("len(Actions) = %d, want 5 (branch, hyperdrive, kv, r2, queue)", len(p.Actions))
	}
	for _, a := range p.Actions {
		if a.Kind != ActionCreate {
			t.Errorf("%s/%s: Kind = %v, want ActionCreate", a.Provider, a.Type, a.Kind)
		}
		if a.Current != nil {
			t.Errorf("%s/%s: Current = %+v, want nil for an absent resource", a.Provider, a.Type, a.Current)
		}
	}
	if !p.HasChanges() {
		t.Error("HasChanges() = false, want true: every action is a Create")
	}
	if p.HasFailures() {
		t.Error("HasFailures() = true, want false")
	}
}

// TestPlan_ExistingResourcesPlanAsNoChange covers a resource Get finds,
// with no ImmutableDiffer opinion, planning as ActionNoChange.
func TestPlan_ExistingResourcesPlanAsNoChange(t *testing.T) {
	f := newRegistryFixture(t)
	m := &manifest.Manifest{
		Root:     manifest.Root{Providers: manifest.Providers{KeyValue: &manifest.Provider{Vendor: "cloudflare"}}},
		Services: map[string]manifest.Service{"api": {KeyValue: []manifest.KeyValue{{Binding: "CACHE"}}}},
	}

	name := naming.ResourceName(envName, "api", "CACHE")
	f.kv.states[name] = &resource.State{Ref: resource.Ref{Provider: "cloudflare", Type: "kv_namespace", Name: name}, ID: "kv-1"}

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	kv := findAction(t, p, "cloudflare", "kv_namespace")
	if kv.Kind != ActionNoChange {
		t.Fatalf("Kind = %v, want ActionNoChange", kv.Kind)
	}
	if kv.Current == nil || kv.Current.ID != "kv-1" {
		t.Fatalf("Current = %+v, want the state Get returned", kv.Current)
	}
	if p.HasChanges() {
		t.Error("HasChanges() = true, want false: the only declared binding exists and is unchanged")
	}
}

// TestPlan_ImmutableDiffPlansAsReplace covers a resource whose registered
// type reports its spec disagrees with the existing state on an immutable
// field.
func TestPlan_ImmutableDiffPlansAsReplace(t *testing.T) {
	differ := &fakeDiffer{
		fakeResource: newFakeResource(),
		differs:      func(resource.Spec, *resource.State) (bool, error) { return true, nil },
	}
	reg := resource.NewRegistry()
	must(t, reg.Register(resource.Registration{
		Provider: "cloudflare", Type: "r2_bucket", Capability: manifest.CapabilityObjects,
		Phase: resource.PhaseStorage, Lookup: resource.LookupByName, Resource: differ,
	}))
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: manifest.Providers{Objects: &manifest.Provider{Vendor: "cloudflare"}}},
		Services: map[string]manifest.Service{
			"api": {Objects: []manifest.ObjectStore{{Binding: "UPLOADS"}}},
		},
	}
	name := naming.ResourceName(envName, "api", "UPLOADS")
	differ.states[name] = &resource.State{Ref: resource.Ref{Provider: "cloudflare", Type: "r2_bucket", Name: name}, ID: "bucket-1"}

	p, err := New(reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := findAction(t, p, "cloudflare", "r2_bucket")
	if got.Kind != ActionReplace {
		t.Fatalf("Kind = %v, want ActionReplace", got.Kind)
	}
	if differ.mutatingCalls() != 0 {
		t.Fatalf("mutating calls = %d, want 0: planning a replace must never call Create/Update/Delete", differ.mutatingCalls())
	}
}

// TestPlan_ImmutableDiffErrorPlansAsFailed covers DiffersFromState itself
// failing.
func TestPlan_ImmutableDiffErrorPlansAsFailed(t *testing.T) {
	f := newRegistryFixture(t)
	boom := errors.New("boom")
	differ := &fakeDiffer{fakeResource: f.r2, differs: func(resource.Spec, *resource.State) (bool, error) { return false, boom }}

	reg := resource.NewRegistry()
	must(t, reg.Register(resource.Registration{
		Provider: "cloudflare", Type: "r2_bucket", Capability: manifest.CapabilityObjects,
		Phase: resource.PhaseStorage, Lookup: resource.LookupByName, Resource: differ,
	}))
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: manifest.Providers{Objects: &manifest.Provider{Vendor: "cloudflare"}}},
		Services: map[string]manifest.Service{
			"api": {Objects: []manifest.ObjectStore{{Binding: "UPLOADS"}}},
		},
	}
	name := naming.ResourceName(envName, "api", "UPLOADS")
	f.r2.states[name] = &resource.State{Ref: resource.Ref{Provider: "cloudflare", Type: "r2_bucket", Name: name}}

	p, err := New(reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := findAction(t, p, "cloudflare", "r2_bucket")
	if got.Kind != ActionFailed || !errors.Is(got.Err, boom) {
		t.Fatalf("action = %+v, want ActionFailed wrapping %v", got, boom)
	}
}

// TestPlan_GetFailureReportsWithoutAbortingTheRun is the partial-failure
// design decision under test: one resource's Get fails, every other
// resource is still planned, and Plan itself returns no error.
func TestPlan_GetFailureReportsWithoutAbortingTheRun(t *testing.T) {
	f := newRegistryFixture(t)
	m := f.oneServiceManifest()

	name := naming.ResourceName(envName, "api", "CACHE")
	boom := errors.New("rate limited")
	f.kv.errs[name] = boom

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 5 {
		t.Fatalf("len(Actions) = %d, want 5: a failed Get must not be dropped from the plan", len(p.Actions))
	}
	kv := findAction(t, p, "cloudflare", "kv_namespace")
	if kv.Kind != ActionFailed || !errors.Is(kv.Err, boom) {
		t.Fatalf("kv action = %+v, want ActionFailed wrapping %v", kv, boom)
	}
	if !p.HasFailures() {
		t.Error("HasFailures() = false, want true")
	}
	// Every other resource, in a different phase and the same phase, still
	// got a real answer.
	for _, other := range []struct{ provider, typ string }{
		{"neon", "branch"}, {"cloudflare", "hyperdrive"}, {"cloudflare", "r2_bucket"}, {"cloudflare", "queue"},
	} {
		a := findAction(t, p, other.provider, other.typ)
		if a.Kind != ActionCreate {
			t.Errorf("%s/%s: Kind = %v, want ActionCreate (unaffected by the kv failure)", other.provider, other.typ, a.Kind)
		}
	}
}

// TestPlan_ConcurrencyLimitBoundsParallelism proves Get calls within a
// phase actually run concurrently (not serially) and never exceed the
// configured limit — the two failure modes D13 exists to rule out.
func TestPlan_ConcurrencyLimitBoundsParallelism(t *testing.T) {
	f := newRegistryFixture(t)
	f.kv.delay = 20 * time.Millisecond

	services := map[string]manifest.Service{}
	var kvBindings []manifest.KeyValue
	const n = 12
	for i := 0; i < n; i++ {
		kvBindings = append(kvBindings, manifest.KeyValue{Binding: bindingName(i)})
	}
	services["api"] = manifest.Service{KeyValue: kvBindings}

	m := &manifest.Manifest{
		Root:     manifest.Root{Providers: manifest.Providers{KeyValue: &manifest.Provider{Vendor: "cloudflare"}}},
		Services: services,
	}

	const limit = 3
	start := time.Now()
	p, err := New(f.reg, WithConcurrency(limit)).Plan(context.Background(), m, envName)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != n {
		t.Fatalf("len(Actions) = %d, want %d", len(p.Actions), n)
	}

	if f.kv.maxInFlight > limit {
		t.Fatalf("maxInFlight = %d, want <= %d: the concurrency limit did not bound parallelism", f.kv.maxInFlight, limit)
	}
	if f.kv.maxInFlight < 2 {
		t.Fatalf("maxInFlight = %d, want >= 2: Get calls ran serially instead of concurrently", f.kv.maxInFlight)
	}
	// n resources at `limit` concurrency take at least ceil(n/limit)
	// delay-sized batches; serial execution would take n batches. This
	// catches a regression to unbounded-but-still-parallel just as well as
	// one to fully serial.
	minBatches := (n + limit - 1) / limit
	if elapsed < time.Duration(minBatches)*f.kv.delay {
		t.Fatalf("elapsed = %v, want >= %v (unbounded concurrency would finish faster than the limit allows)",
			elapsed, time.Duration(minBatches)*f.kv.delay)
	}
}

// TestPlan_NeverCallsMutatingVerbs runs a scenario that hits every
// ActionKind (create, no-change, replace, failed) and asserts the
// mutating verbs were never called on any registered resource — the
// package's structural claim, checked behaviourally.
func TestPlan_NeverCallsMutatingVerbs(t *testing.T) {
	f := newRegistryFixture(t)
	m := f.oneServiceManifest()

	branchName := naming.ResourceName(envName, "api", "DB")
	f.branch.states[branchName] = &resource.State{Ref: resource.Ref{Provider: "neon", Type: "branch", Name: branchName}}
	kvName := naming.ResourceName(envName, "api", "CACHE")
	f.kv.errs[kvName] = errors.New("boom")

	if _, err := New(f.reg).Plan(context.Background(), m, envName); err != nil {
		t.Fatalf("Plan: %v", err)
	}

	for name, fr := range map[string]*fakeResource{
		"branch": f.branch, "hyperdrive": f.hyperdrive, "kv": f.kv, "r2": f.r2, "queue": f.queue,
	} {
		if got := fr.mutatingCalls(); got != 0 {
			t.Errorf("%s: mutating calls = %d, want 0", name, got)
		}
	}
}

func TestPlan_NilManifestIsValidationError(t *testing.T) {
	f := newRegistryFixture(t)
	_, err := New(f.reg).Plan(context.Background(), nil, envName)
	assertValidationError(t, err, "manifest is nil")
}

func TestPlan_EmptyEnvironmentNameIsValidationError(t *testing.T) {
	f := newRegistryFixture(t)
	_, err := New(f.reg).Plan(context.Background(), f.oneServiceManifest(), "")
	assertValidationError(t, err, "environment name")
}

func TestPlan_UnconfiguredCapabilityIsValidationError(t *testing.T) {
	f := newRegistryFixture(t)
	m := &manifest.Manifest{
		Services: map[string]manifest.Service{"api": {Databases: []manifest.Database{{Binding: "DB", Engine: "postgres"}}}},
	}
	_, err := New(f.reg).Plan(context.Background(), m, envName)
	assertValidationError(t, err, "services.api.databases.DB")
}

func TestPlan_UnknownVendorIsValidationError(t *testing.T) {
	f := newRegistryFixture(t)
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: manifest.Providers{Postgres: &manifest.Provider{Vendor: "aws"}}},
		Services: map[string]manifest.Service{
			"api": {Databases: []manifest.Database{{Binding: "DB", Engine: "postgres"}}},
		},
	}
	_, err := New(f.reg).Plan(context.Background(), m, envName)
	assertValidationError(t, err, "services.api.databases.DB")
}

func TestPlan_UnconfiguredCapability_EveryBindingKind(t *testing.T) {
	f := newRegistryFixture(t)
	cases := []struct {
		name    string
		service manifest.Service
		wantErr string
	}{
		{"keyvalue", manifest.Service{KeyValue: []manifest.KeyValue{{Binding: "CACHE"}}}, "services.api.keyvalue.CACHE"},
		{"objects", manifest.Service{Objects: []manifest.ObjectStore{{Binding: "UPLOADS"}}}, "services.api.objects.UPLOADS"},
		{"queues", manifest.Service{Queues: []manifest.Queue{{Binding: "JOBS"}}}, "services.api.queues.JOBS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &manifest.Manifest{Services: map[string]manifest.Service{"api": c.service}}
			_, err := New(f.reg).Plan(context.Background(), m, envName)
			assertValidationError(t, err, c.wantErr)
		})
	}
}

// TestPlan_DatabaseCachingIsCarriedIntoConfig covers a Database binding
// that declares caching, whose config a future resource type could read
// (Spec.Config is intentionally opaque to this package; see resource.Spec).
func TestPlan_DatabaseCachingIsCarriedIntoConfig(t *testing.T) {
	f := newRegistryFixture(t)
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: manifest.Providers{Postgres: &manifest.Provider{Vendor: "neon"}}},
		Services: map[string]manifest.Service{
			"api": {Databases: []manifest.Database{{
				Binding: "DB", Engine: "postgres", Caching: &manifest.Caching{Disabled: true, MaxAge: 30},
			}}},
		},
	}
	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	branch := findAction(t, p, "neon", "branch")
	caching, ok := branch.Spec.Config["caching"].(manifest.Caching)
	if !ok || caching.MaxAge != 30 || !caching.Disabled {
		t.Fatalf("Spec.Config[caching] = %#v, want the declared Caching value", branch.Spec.Config["caching"])
	}
}

func TestPlan_MultipleServicesAreOrderedDeterministically(t *testing.T) {
	f := newRegistryFixture(t)
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: f.providers()},
		Services: map[string]manifest.Service{
			"zeta":  {KeyValue: []manifest.KeyValue{{Binding: "CACHE"}}},
			"alpha": {KeyValue: []manifest.KeyValue{{Binding: "CACHE"}}},
		},
	}
	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 2 {
		t.Fatalf("len(Actions) = %d, want 2", len(p.Actions))
	}
	if p.Actions[0].ServiceKey != "alpha" || p.Actions[1].ServiceKey != "zeta" {
		t.Fatalf("service order = [%s, %s], want [alpha, zeta]", p.Actions[0].ServiceKey, p.Actions[1].ServiceKey)
	}
}

func TestPlan_EmptyManifestProducesEmptyPlan(t *testing.T) {
	f := newRegistryFixture(t)
	m := &manifest.Manifest{Root: manifest.Root{Providers: f.providers()}, Services: map[string]manifest.Service{}}
	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) != 0 {
		t.Fatalf("len(Actions) = %d, want 0", len(p.Actions))
	}
	if p.HasChanges() || p.HasFailures() {
		t.Fatalf("HasChanges/HasFailures on an empty plan should both be false")
	}
}

func TestWithConcurrency_IgnoresNonPositive(t *testing.T) {
	p := New(resource.NewRegistry(), WithConcurrency(0))
	if p.concurrency != defaultConcurrency {
		t.Fatalf("concurrency = %d, want default %d for a non-positive override", p.concurrency, defaultConcurrency)
	}
	p = New(resource.NewRegistry(), WithConcurrency(-5))
	if p.concurrency != defaultConcurrency {
		t.Fatalf("concurrency = %d, want default %d for a negative override", p.concurrency, defaultConcurrency)
	}
	p = New(resource.NewRegistry(), WithConcurrency(4))
	if p.concurrency != 4 {
		t.Fatalf("concurrency = %d, want 4", p.concurrency)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertValidationError(t *testing.T, err error, wantSubstr string) {
	t.Helper()
	if err == nil {
		t.Fatal("err = nil, want a validation error")
	}
	var kerr *kerrors.KError
	if !errors.As(err, &kerr) || kerr.Code() != kerrors.CodeValidation {
		t.Fatalf("err = %v, want a kerrors.CodeValidation error", err)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("err = %q, want it to mention %q", err.Error(), wantSubstr)
	}
}

func bindingName(i int) string {
	return fmt.Sprintf("BINDING_%d", i)
}

// TestPlanReportsCancellationRatherThanFailures: every Get honours ctx, so a
// cancelled walk leaves an Action per resource saying it could not be read.
// Returning that as a plan renders a wall of failures that reads as "your
// infrastructure is unreachable" rather than "you pressed Ctrl-C", and the
// reader cannot tell which entries are real.
func TestPlanReportsCancellationRatherThanFailures(t *testing.T) {
	f := newRegistryFixture(t)
	for _, r := range []*fakeResource{f.branch, f.hyperdrive, f.kv, f.r2, f.queue} {
		r.delay = 50 * time.Millisecond
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := New(f.reg).Plan(ctx, f.oneServiceManifest(), "env-a")
	if err == nil {
		kinds := map[ActionKind]int{}
		for _, a := range got.Actions {
			kinds[a.Kind]++
		}
		t.Fatalf("a cancelled plan returned %d actions (%v) and no error", len(got.Actions), kinds)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want the cancellation to surface", err)
	}
	if got != nil {
		t.Fatalf("a cancelled plan returned a Plan alongside its error: %+v", got)
	}
}

// TestPostgresExpandsAcrossProviders is what the registry fix makes possible:
// one vendor choice reaching both halves of the capability, without the
// planner supplementing the registry itself.
func TestPostgresExpandsAcrossProviders(t *testing.T) {
	f := newRegistryFixture(t)

	got, err := New(f.reg).Plan(t.Context(), f.oneServiceManifest(), "env-a")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var branch, hyperdrive int
	for _, a := range got.Actions {
		switch a.Type {
		case "branch":
			branch++
		case "hyperdrive":
			hyperdrive++
		}
	}
	if branch != 1 || hyperdrive != 1 {
		t.Fatalf("one postgres binding planned %d branch and %d hyperdrive actions, want one each",
			branch, hyperdrive)
	}
	if f.branch.getCalls != 1 || f.hyperdrive.getCalls != 1 {
		t.Fatalf("Get calls: branch=%d hyperdrive=%d", f.branch.getCalls, f.hyperdrive.getCalls)
	}
}
