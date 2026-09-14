package resource

import (
	"sort"
	"sync"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// Phase orders provisioning (Q2, approved 2026-09-14).
//
// # Why phases rather than a dependency graph
//
// Resources genuinely depend on each other — a Hyperdrive configuration needs
// the connection string of the database branch it fronts, so the branch has
// to exist first. The JavaScript encoded that by hardcoding the order:
// provider, then per-service resources, then deploy.
//
// A general dependency graph would express this too, and buys flexibility
// nothing in kraai currently needs, at the cost of cycle detection,
// partial-failure semantics across an arbitrary topology, and a much harder
// model for anyone adding a resource type. Phases keep the ordering
// declarative and legible: a registration names when it runs, the applier
// runs phases in sequence and everything within a phase in parallel under a
// global concurrency limit (D13).
//
// Teardown runs phases in reverse, which is what the JavaScript's own
// teardown order already was.
type Phase int

const (
	// PhaseDatabase provisions databases and their branches. First, because
	// everything that binds to a database needs its connection details.
	PhaseDatabase Phase = iota
	// PhaseStorage provisions per-service storage: key-value, object, queue.
	PhaseStorage
	// PhaseCompute deploys the code that binds to everything above.
	PhaseCompute
)

// phaseNames is used for messages and span attributes.
var phaseNames = map[Phase]string{
	PhaseDatabase: "database",
	PhaseStorage:  "storage",
	PhaseCompute:  "compute",
}

func (p Phase) String() string {
	if name, ok := phaseNames[p]; ok {
		return name
	}
	return "unknown"
}

// Valid reports whether p is a declared phase.
func (p Phase) Valid() bool {
	_, ok := phaseNames[p]
	return ok
}

// Phases returns every phase in provisioning order.
func Phases() []Phase { return []Phase{PhaseDatabase, PhaseStorage, PhaseCompute} }

// Registration is one resource type's entry in the registry.
type Registration struct {
	// Provider and Type form the registry key: whose API this calls.
	Provider string
	Type     string
	// Vendor is the manifest `vendor:` value that selects this registration.
	// Empty means Provider, which is the common case.
	//
	// The two differ when fulfilling a capability takes resources from more
	// than one API. Choosing Neon for Postgres also requires a Cloudflare
	// Hyperdrive configuration in front of it: that registration's Provider
	// is "cloudflare", because that is whose API creates it, but its Vendor
	// is "neon", because choosing Neon is what asks for it. Without the
	// distinction a manifest saying `postgres: {vendor: neon}` resolves to
	// the branch alone and the configuration fronting it is never planned —
	// which is exactly what happened, and what a test in the adapter that
	// introduced it wrongly asserted as correct.
	Vendor string
	// Capability is what this type fulfils in a manifest — "postgres",
	// "keyvalue", "objects", "queues", "compute". It is how a manifest entry
	// that names no vendor reaches a vendor's implementation (Q1).
	Capability string
	// Phase is when this type is provisioned.
	Phase Phase
	// Lookup is how instances are found (D26).
	Lookup LookupStrategy
	// Resource implements the verbs.
	Resource Resource
}

// Key is the registry key, "provider/type".
func (r Registration) Key() string { return r.Provider + "/" + r.Type }

// vendor is the manifest value that selects this registration.
func (r Registration) vendor() string {
	if r.Vendor != "" {
		return r.Vendor
	}
	return r.Provider
}

// Registry maps provider/type to an implementation, and capability plus
// vendor to the types that fulfil it.
//
// Plugin-provided and compiled-in types register identically, so nothing
// downstream can tell them apart — and a plugin type is instrumented by the
// same decorator as a built-in rather than being invisible to telemetry.
type Registry struct {
	mu sync.RWMutex
	// byKey is provider/type -> registration.
	byKey map[string]Registration
	// byCapability is capability -> vendor -> registrations, in registration
	// order. Keyed by vendor rather than provider so one manifest choice
	// reaches every resource that choice implies — see Registration.Vendor.
	byCapability map[string]map[string][]Registration
	// decorate wraps every Resource at registration time (D17).
	decorate func(Registration) Resource
}

// Option configures a Registry.
type Option func(*Registry)

// WithDecorator sets what wraps each registered Resource. Applied at
// registration rather than at call time, so no resource type can be added
// without instrumentation by forgetting a wrapper.
func WithDecorator(d func(Registration) Resource) Option {
	return func(r *Registry) { r.decorate = d }
}

// NewRegistry builds an empty registry.
func NewRegistry(opts ...Option) *Registry {
	r := &Registry{
		byKey:        map[string]Registration{},
		byCapability: map[string]map[string][]Registration{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a resource type.
//
// Registering the same provider/type twice is an error rather than a silent
// overwrite. A plugin shadowing a built-in is a real scenario — the plugin
// runtime supports overriding deliberately — but that has to be an explicit
// act at the point the registry is assembled, not a side effect of load
// order, which would make behaviour depend on which plugin happened to be
// listed first.
func (r *Registry) Register(reg Registration) error {
	if err := validate(reg); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byKey[reg.Key()]; exists {
		return kerrors.Validation(
			"resource type %q is already registered — override it explicitly rather than "+
				"registering it twice", reg.Key())
	}

	if r.decorate != nil {
		reg.Resource = r.decorate(reg)
	}
	r.byKey[reg.Key()] = reg

	byVendor, ok := r.byCapability[reg.Capability]
	if !ok {
		byVendor = map[string][]Registration{}
		r.byCapability[reg.Capability] = byVendor
	}
	byVendor[reg.vendor()] = append(byVendor[reg.vendor()], reg)
	return nil
}

// validate rejects a registration that could not work, at the point it is
// added rather than the point it is first used.
func validate(reg Registration) error {
	switch {
	case reg.Provider == "":
		return kerrors.Validation("resource registration has no Provider")
	case reg.Type == "":
		return kerrors.Validation("resource registration %q has no Type", reg.Provider)
	case reg.Capability == "":
		return kerrors.Validation("resource registration %q has no Capability", reg.Provider+"/"+reg.Type)
	case reg.Resource == nil:
		return kerrors.Validation("resource registration %q has no Resource", reg.Provider+"/"+reg.Type)
	case !reg.Lookup.Valid():
		return kerrors.Validation(
			"resource registration %q declares unknown lookup strategy %q",
			reg.Provider+"/"+reg.Type, reg.Lookup)
	case !reg.Phase.Valid():
		return kerrors.Validation(
			"resource registration %q declares unknown phase %d", reg.Provider+"/"+reg.Type, reg.Phase)
	}
	return nil
}

// Lookup returns the registration for a provider/type key.
func (r *Registry) Lookup(key string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.byKey[key]
	return reg, ok
}

// Resolve returns every registration a vendor choice implies for a capability
// (D30).
//
// A manifest entry names a capability and kraai.yaml names the vendor that
// fulfils it. One such pair can expand to more than one resource, and those
// resources need not come from the same API: choosing Neon for Postgres means
// a Neon branch and the Cloudflare Hyperdrive configuration fronting it. Both
// are returned, because both are what that one choice asked for.
//
// Returned in phase order so the caller does not have to sort them, and
// within a phase in registration order so expansion is deterministic.
func (r *Registry) Resolve(capability, vendor string) ([]Registration, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	byVendor, ok := r.byCapability[capability]
	if !ok {
		return nil, kerrors.Validation(
			"no resource type provides capability %q — known capabilities: %s",
			capability, join(capabilityNames(r.byCapability)))
	}
	regs, ok := byVendor[vendor]
	if !ok || len(regs) == 0 {
		return nil, kerrors.Validation(
			"vendor %q does not provide capability %q — vendors for it: %s",
			vendor, capability, join(providerNames(byVendor)))
	}

	out := append([]Registration(nil), regs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Phase < out[j].Phase })
	return out, nil
}

// All returns every registration, in phase then key order, so a caller
// iterating the registry gets a stable sequence.
func (r *Registry) All() []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Registration, 0, len(r.byKey))
	for _, reg := range r.byKey {
		out = append(out, reg)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].Key() < out[j].Key()
	})
	return out
}

func capabilityNames(m map[string]map[string][]Registration) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func providerNames(m map[string][]Registration) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func join(names []string) string {
	if len(names) == 0 {
		return "(none registered)"
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	return out
}
