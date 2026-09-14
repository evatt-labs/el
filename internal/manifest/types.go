package manifest

// Manifest is the fully-resolved, validated manifest for one environment:
// kraai.yaml's root config, every services/*.yaml file merged into one
// service set (D4), the environment overlay, and the merged values map
// (values file + --set, D5) that was used as template context.
type Manifest struct {
	Root        Root
	Services    map[string]Service
	Environment Environment
	// Values is the merged environments/<name>.values.yaml + --set map.
	// Deliberately map[string]any: values files are free-form and exempt
	// from schema validation (D5), unlike every other field here.
	Values map[string]any
}

// Root is kraai.yaml at the manifest root (D4): providers, hooks, plugins.
// See docs/BLUEPRINT.md "Manifest schema" for the canonical example.
type Root struct {
	Version   int       `yaml:"version"`
	Providers Providers `yaml:"providers"`
	Hooks     string    `yaml:"hooks,omitempty"`
	Plugins   []string  `yaml:"plugins,omitempty"`
}

// Providers names which vendor fulfils each capability kraai.yaml declares,
// and carries that vendor's own settings.
//
// A fixed struct rather than a free-form map, so an unknown capability is
// rejected at load rather than silently ignored until something fails to
// resolve. The vocabulary here is no longer a guess: it is exactly the set of
// capabilities the resource registry has implementations for.
type Providers struct {
	Compute  *Provider `yaml:"compute,omitempty"`
	Database *Provider `yaml:"database,omitempty"`
	KeyValue *Provider `yaml:"keyvalue,omitempty"`
	Objects  *Provider `yaml:"objects,omitempty"`
	Queues   *Provider `yaml:"queues,omitempty"`
}

// Capability names, matching the keys above and the capabilities resource
// registrations declare. Exported so a caller resolving a manifest entry to a
// provider uses the same strings the registry does, rather than a second copy
// that can drift.
const (
	CapabilityCompute = "compute"
	// CapabilityDatabase covers every database engine, not one of them. A
	// service's `databases:` entry already carries an `engine`, so the
	// capability naming a specific engine would encode the same fact twice
	// and, worse, leave engines with no capability at all: Cloudflare D1
	// registered under "database" and was unreachable, because the only
	// database capability the manifest offered was "postgres".
	CapabilityDatabase = "database"
	CapabilityKeyValue = "keyvalue"
	CapabilityObjects  = "objects"
	CapabilityQueues   = "queues"
)

// Provider is one capability's vendor and that vendor's configuration.
//
// # Why Settings is free-form
//
// Which Neon project to branch from, which AWS region to deploy into, which
// Lambda runtime to use — none of that belongs in this package's vocabulary,
// and encoding it here would put every vendor's fields in the one type whose
// purpose is not having them. Settings is passed to the provider, which
// decodes and validates its own shape and reports its own errors.
//
// The same exemption Values carries (D5), for the same reason and with the
// same cost: this is the one part of a manifest not checked at load.
type Provider struct {
	// Vendor is the implementation fulfilling the capability, e.g. "neon".
	Vendor string `yaml:"vendor"`
	// Settings is the vendor's own configuration, uninterpreted here.
	Settings map[string]any `yaml:"settings,omitempty"`
}

// For returns the provider configured for a capability.
//
// A method rather than each caller switching on field names: the switch
// belongs in one place, and a capability added to the struct without a case
// here is a compile-time-visible omission instead of a silent nil.
func (p Providers) For(capability string) (*Provider, bool) {
	var configured *Provider
	switch capability {
	case CapabilityCompute:
		configured = p.Compute
	case CapabilityDatabase:
		configured = p.Database
	case CapabilityKeyValue:
		configured = p.KeyValue
	case CapabilityObjects:
		configured = p.Objects
	case CapabilityQueues:
		configured = p.Queues
	default:
		return nil, false
	}
	if configured == nil {
		return nil, false
	}
	return configured, true
}

// Vendors maps each configured capability to the vendor fulfilling it.
//
// This is what a resource registry resolves against: a registration can
// declare a condition on a capability other than its own — a Cloudflare
// Hyperdrive config belongs to a database binding but only applies when
// compute is also Cloudflare — and answering that needs the whole set, not
// one entry.
func (p Providers) Vendors() map[string]string {
	out := make(map[string]string, 5)
	for _, capability := range p.Capabilities() {
		configured, _ := p.For(capability)
		out[capability] = configured.Vendor
	}
	return out
}

// Capabilities returns the capabilities this manifest configures, in a stable
// order.
func (p Providers) Capabilities() []string {
	var out []string
	for _, capability := range []string{
		CapabilityCompute, CapabilityDatabase,
		CapabilityKeyValue, CapabilityObjects, CapabilityQueues,
	} {
		if _, ok := p.For(capability); ok {
			out = append(out, capability)
		}
	}
	return out
}

// ServicesFile is the shape of one services/*.yaml (or .yaml.j2) file
// before merging (D4). Multiple files are globbed and merged into a single
// map[string]Service by the loader; see mergeServiceFiles.
type ServicesFile struct {
	Services map[string]Service `yaml:"services"`
}

// Service is one service entry under services/*.yaml's top-level
// `services:` map.
type Service struct {
	Dir       string        `yaml:"dir"`
	Databases []Database    `yaml:"databases,omitempty"`
	KeyValue  []KeyValue    `yaml:"keyvalue,omitempty"`
	Objects   []ObjectStore `yaml:"objects,omitempty"`
	Queues    []Queue       `yaml:"queues,omitempty"`
}

// Database is one entry of a service's `databases:` list.
//
// Driver is what the application connects with — the wire protocol and
// client library — not which product implements it. That is the distinction
// the field exists to carry: Neon is Postgres-wire and Cloudflare D1 is
// SQLite-wire, and an application cares which of those it is speaking, not
// whose storage is underneath. `driver: postgres` is therefore a claim about
// the connection the service expects, which a provider can honour or refuse.
//
// Free-form rather than an enum: per-resource schemas are generated from each
// provider's own machine-readable source in a later workstream, never
// hand-transcribed, so this package does not own that vocabulary.
type Database struct {
	Binding string   `yaml:"binding"`
	Driver  string   `yaml:"driver"`
	Caching *Caching `yaml:"caching,omitempty"`
}

// Caching configures a Database binding's cache behavior.
type Caching struct {
	Disabled bool `yaml:"disabled"`
	MaxAge   int  `yaml:"maxAge"`
}

// KeyValue is one entry of a service's `keyvalue:` list.
type KeyValue struct {
	Binding string `yaml:"binding"`
}

// ObjectStore is one entry of a service's `objects:` list.
type ObjectStore struct {
	Binding string `yaml:"binding"`
}

// Queue is one entry of a service's `queues:` list.
type Queue struct {
	Binding  string `yaml:"binding"`
	Consumer bool   `yaml:"consumer,omitempty"`
}

// Environment is environments/<name>.yaml: the overlay describing one
// environment's kind, protection, naming, routes, and imported resources
// (D7). Never templated (D5 names only kraai.yaml.j2 and
// services/*.yaml.j2 as opt-in-templated); always schema-validated.
type Environment struct {
	// Kind is "ephemeral" or "persistent" — validated in Validate.
	Kind      string                     `yaml:"kind"`
	Protected bool                       `yaml:"protected,omitempty"`
	Naming    *Naming                    `yaml:"naming,omitempty"`
	Routes    map[string][]Route         `yaml:"routes,omitempty"`
	Resources map[string]ResourceImports `yaml:"resources,omitempty"`
}

// EnvironmentKindEphemeral and EnvironmentKindPersistent are Environment's
// only valid Kind values.
const (
	EnvironmentKindEphemeral  = "ephemeral"
	EnvironmentKindPersistent = "persistent"
)

// Naming configures a persistent environment's naming overlay.
type Naming struct {
	Prefix string `yaml:"prefix"`
}

// Route is one entry of a service's `routes:` list on an environment
// overlay.
type Route struct {
	Pattern      string `yaml:"pattern"`
	CustomDomain bool   `yaml:"custom_domain,omitempty"`
}

// ResourceImports is one service's imported/adopted resources (D7, D8):
// the reference written directly into the manifest is the resource's
// identity, keyed by binding name within each resource kind.
type ResourceImports struct {
	Databases map[string]ImportRef `yaml:"databases,omitempty"`
	KeyValue  map[string]ImportRef `yaml:"keyvalue,omitempty"`
	Objects   map[string]ImportRef `yaml:"objects,omitempty"`
	Queues    map[string]ImportRef `yaml:"queues,omitempty"`
}

// ImportRef identifies a pre-existing, adopted resource (D7): either an id
// or a name, whichever the provider's own lookup needs.
type ImportRef struct {
	ID   string `yaml:"id,omitempty"`
	Name string `yaml:"name,omitempty"`
}
