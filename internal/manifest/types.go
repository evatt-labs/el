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

// Providers names which vendor fulfils each capability kraai.yaml declares.
// Fixed to the two capabilities BLUEPRINT.md's example documents (compute,
// postgres) rather than a free-form map: BLUEPRINT.md's own open question 1
// flags that this vocabulary was never re-confirmed for the Go rewrite, but
// until it changes, a concrete struct is what "unknown keys rejected at
// every level" and "no map[string]any as the resolved model" (D21) both
// call for. Widening this is a one-line change when the vocabulary is
// revisited.
type Providers struct {
	Compute  string `yaml:"compute,omitempty"`
	Postgres string `yaml:"postgres,omitempty"`
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

// Database is one entry of a service's `databases:` list. Engine is a
// free-form string, not an enum: BLUEPRINT.md's "Manifest schema" section
// notes per-resource schemas are generated from each provider's own
// machine-readable source in a later workstream, never hand-transcribed —
// this workstream doesn't own that closed vocabulary.
type Database struct {
	Binding string   `yaml:"binding"`
	Engine  string   `yaml:"engine"`
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
