package plan

import (
	"context"
	"testing"

	"github.com/evatt-labs/kraai/internal/manifest"
	"github.com/evatt-labs/kraai/internal/naming"
	"github.com/evatt-labs/kraai/internal/resource"
)

// computeRegistryFixture registers two compute types shaped exactly like
// internal/provider/aws's real registrations: a function type with no
// Triggers restriction (applies regardless of trigger) and an HTTP-gated
// type restricted to manifest.TriggerHTTP — the minimal shape that
// reproduces the bug this workstream fixes without importing the aws
// package itself (D21: providers are exercised through fakes, not real
// clients).
type computeRegistryFixture struct {
	reg      *resource.Registry
	function *fakeResource
	httpAPI  *fakeResource
}

func newComputeRegistryFixture(t *testing.T) *computeRegistryFixture {
	t.Helper()

	f := &computeRegistryFixture{
		reg:      resource.NewRegistry(),
		function: newFakeResource(),
		httpAPI:  newFakeResource(),
	}

	regs := []resource.Registration{
		{
			Provider: "fakecloud", Type: "function", Capability: manifest.CapabilityCompute,
			Phase: resource.PhaseCompute, Lookup: resource.LookupByName, Resource: f.function,
			// No Triggers: applies to every service using this compute
			// vendor, whatever it declares (or doesn't declare).
		},
		{
			Provider: "fakecloud", Type: "http_api", Capability: manifest.CapabilityCompute,
			Phase: resource.PhaseCompute, Lookup: resource.LookupByName, Resource: f.httpAPI,
			Triggers: []string{manifest.TriggerHTTP},
		},
	}
	for _, r := range regs {
		if err := f.reg.Register(r); err != nil {
			t.Fatalf("Register(%s): %v", r.Key(), err)
		}
	}
	return f
}

func (f *computeRegistryFixture) providers() manifest.Providers {
	return manifest.Providers{
		Compute: &manifest.Provider{
			Vendor:   "fakecloud",
			Settings: map[string]any{"runtime": "python3.14", "architecture": "arm64", "reservedConcurrency": 5},
		},
	}
}

// TestPlan_TriggerGatesComputeResourceTypes is this workstream's own
// acceptance criterion: against a kraai-api-shaped manifest (an
// HTTP-triggered `api` and a schedule-triggered `tick`, both using the same
// compute vendor), `tick` plans one function and no HTTP API, while `api`
// plans both — driven by each service's declared trigger, not by which
// types happen to be registered for the vendor.
func TestPlan_TriggerGatesComputeResourceTypes(t *testing.T) {
	f := newComputeRegistryFixture(t)
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: f.providers()},
		Services: map[string]manifest.Service{
			"api": {
				Dir:     ".",
				Compute: &manifest.Compute{Trigger: manifest.TriggerHTTP, Handler: "app.main.handler"},
			},
			"tick": {
				Dir: ".",
				Compute: &manifest.Compute{
					Trigger: manifest.TriggerSchedule, Handler: "app.tasks.tick.handler", Schedule: "rate(5 minutes)",
				},
			},
		},
	}

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	tickTypes := actionTypesFor(p, "tick")
	if len(tickTypes) != 1 || !tickTypes["function"] {
		t.Fatalf("tick's planned types = %v, want exactly {function}: a schedule-triggered service must not plan an HTTP API", tickTypes)
	}

	apiTypes := actionTypesFor(p, "api")
	if len(apiTypes) != 2 || !apiTypes["function"] || !apiTypes["http_api"] {
		t.Fatalf("api's planned types = %v, want {function, http_api}", apiTypes)
	}
}

// TestPlan_NoComputeBlockAppliesEveryRegisteredType is the required
// backward-compatibility guarantee: a service that never opts into the new
// compute: schema must plan exactly as it always has — one resource per
// type the vendor registers for the capability, unconditioned on trigger.
func TestPlan_NoComputeBlockAppliesEveryRegisteredType(t *testing.T) {
	f := newComputeRegistryFixture(t)
	m := &manifest.Manifest{
		Root:     manifest.Root{Providers: f.providers()},
		Services: map[string]manifest.Service{"legacy": {Dir: "."}},
	}

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	types := actionTypesFor(p, "legacy")
	if len(types) != 2 || !types["function"] || !types["http_api"] {
		t.Fatalf("legacy's planned types = %v, want both {function, http_api} unchanged from before Triggers existed", types)
	}
}

// TestPlan_ComputeSettingsMergeIntoSpecConfig proves expandCompute reaches
// both halves of the merge: the provider's settings survive where the
// service is silent, and the service's own key wins where it isn't.
func TestPlan_ComputeSettingsMergeIntoSpecConfig(t *testing.T) {
	f := newComputeRegistryFixture(t)
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: f.providers()},
		Services: map[string]manifest.Service{
			"tick": {
				Dir: ".",
				Compute: &manifest.Compute{
					Trigger: manifest.TriggerSchedule, Schedule: "rate(5 minutes)",
					Settings: map[string]any{"reservedConcurrency": 1},
				},
			},
		},
	}

	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	fn := findAction(t, p, "fakecloud", "function")
	settings, ok := fn.Spec.Config["settings"].(map[string]any)
	if !ok {
		t.Fatalf("Spec.Config[settings] = %#v, want a map", fn.Spec.Config["settings"])
	}
	if settings["reservedConcurrency"] != 1 {
		t.Errorf("settings[reservedConcurrency] = %v, want the service's override (1)", settings["reservedConcurrency"])
	}
	if settings["runtime"] != "python3.14" || settings["architecture"] != "arm64" {
		t.Errorf("provider-only settings did not survive the merge: %+v", settings)
	}
	if fn.Spec.Config["trigger"] != manifest.TriggerSchedule {
		t.Errorf("Spec.Config[trigger] = %v", fn.Spec.Config["trigger"])
	}
	if fn.Spec.Config["schedule"] != "rate(5 minutes)" {
		t.Errorf("Spec.Config[schedule] = %v", fn.Spec.Config["schedule"])
	}
	if _, present := fn.Spec.Config["handler"]; present {
		t.Errorf("Spec.Config[handler] = %v, want absent: this service never set one", fn.Spec.Config["handler"])
	}
}

// TestPlan_ComputeNameIsPerServiceNotPerBinding pins that the trigger
// filter operates on the compute resource's own name (naming.ServiceName)
// and does not disturb the deployable-unit-per-service identity D36
// established.
func TestPlan_ComputeNameIsPerServiceNotPerBinding(t *testing.T) {
	f := newComputeRegistryFixture(t)
	m := &manifest.Manifest{
		Root: manifest.Root{Providers: f.providers()},
		Services: map[string]manifest.Service{
			"tick": {Dir: ".", Compute: &manifest.Compute{Trigger: manifest.TriggerSchedule}},
		},
	}
	p, err := New(f.reg).Plan(context.Background(), m, envName)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	fn := findAction(t, p, "fakecloud", "function")
	want := naming.ServiceName(envName, "tick")
	if fn.Ref.Name != want {
		t.Errorf("Ref.Name = %q, want %q", fn.Ref.Name, want)
	}
}

// actionTypesFor returns the set of resource.Registration.Type values
// planned for one service key.
func actionTypesFor(p *Plan, svcKey string) map[string]bool {
	out := map[string]bool{}
	for _, a := range p.Actions {
		if a.ServiceKey == svcKey {
			out[a.Type] = true
		}
	}
	return out
}
