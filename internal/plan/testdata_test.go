package plan

import (
	"testing"

	"github.com/evatt-labs/kraai/internal/manifest"
	"github.com/evatt-labs/kraai/internal/resource"
)

// registryFixture is a stand-in registry shaped like the real one: a
// Postgres capability that expands across two providers the way
// internal/provider/neonresource actually registers it (D30), plus one
// single-type capability per other manifest resource kind.
type registryFixture struct {
	reg *resource.Registry

	branch     *fakeResource
	hyperdrive *fakeResource
	kv         *fakeResource
	r2         *fakeResource
	queue      *fakeResource
}

func newRegistryFixture(t *testing.T) *registryFixture {
	t.Helper()

	f := &registryFixture{
		reg:        resource.NewRegistry(),
		branch:     newFakeResource(),
		hyperdrive: newFakeResource(),
		kv:         newFakeResource(),
		r2:         newFakeResource(),
		queue:      newFakeResource(),
	}

	regs := []resource.Registration{
		{
			Provider: "neon", Type: "branch", Capability: manifest.CapabilityPostgres,
			Phase: resource.PhaseDatabase, Lookup: resource.LookupByAttr, Resource: f.branch,
		},
		{
			// A different literal Provider than the branch above, on
			// purpose: this is the exact shape
			// internal/provider/neonresource.Register uses for Hyperdrive
			// (D30). Cloudflare's API creates it, but choosing Neon for
			// Postgres is what asks for it, so Vendor says neon — without
			// which one vendor choice reaches only half the capability.
			Provider: "cloudflare", Type: "hyperdrive", Vendor: "neon",
			Capability: manifest.CapabilityPostgres,
			Phase:      resource.PhaseStorage, Lookup: resource.LookupByAttr, Resource: f.hyperdrive,
		},
		{
			Provider: "cloudflare", Type: "kv_namespace", Capability: manifest.CapabilityKeyValue,
			Phase: resource.PhaseStorage, Lookup: resource.LookupByAttr, Resource: f.kv,
		},
		{
			Provider: "cloudflare", Type: "r2_bucket", Capability: manifest.CapabilityObjects,
			Phase: resource.PhaseStorage, Lookup: resource.LookupByName, Resource: f.r2,
		},
		{
			Provider: "cloudflare", Type: "queue", Capability: manifest.CapabilityQueues,
			Phase: resource.PhaseStorage, Lookup: resource.LookupByAttr, Resource: f.queue,
		},
	}
	for _, r := range regs {
		if err := f.reg.Register(r); err != nil {
			t.Fatalf("Register(%s): %v", r.Key(), err)
		}
	}
	return f
}

// providers builds the Providers block naming this fixture's vendors, so
// callers don't repeat the same four lines in every test.
func (f *registryFixture) providers() manifest.Providers {
	return manifest.Providers{
		Postgres: &manifest.Provider{Vendor: "neon"},
		KeyValue: &manifest.Provider{Vendor: "cloudflare"},
		Objects:  &manifest.Provider{Vendor: "cloudflare"},
		Queues:   &manifest.Provider{Vendor: "cloudflare"},
	}
}

// oneServiceManifest builds a minimal manifest with one service declaring
// one binding of each kind, wired to this fixture's vendors.
func (f *registryFixture) oneServiceManifest() *manifest.Manifest {
	return &manifest.Manifest{
		Root: manifest.Root{Providers: f.providers()},
		Services: map[string]manifest.Service{
			"api": {
				Databases: []manifest.Database{{Binding: "DB", Engine: "postgres"}},
				KeyValue:  []manifest.KeyValue{{Binding: "CACHE"}},
				Objects:   []manifest.ObjectStore{{Binding: "UPLOADS"}},
				Queues:    []manifest.Queue{{Binding: "JOBS", Consumer: true}},
			},
		},
	}
}
