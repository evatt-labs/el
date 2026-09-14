// Package resource is the contract every provisioned thing implements, and
// the registry that maps a manifest entry to the code that fulfils it.
//
// It is the seam between a deliberately vendor-neutral manifest — a service
// declares databases, keyvalue, objects and queues, never "a D1 database" —
// and the provider clients that know what those actually are. Everything
// above this package reasons about resources; everything below reasons about
// one cloud's API.
//
// # Shape
//
// Per-verb methods rather than one Ensure (D15). Plan calls Get and diffs
// without touching the mutating verbs, and each verb gets its own span and
// its own timing (D17) — a single Ensure blurs all of that into one
// measurement with no way to see which sub-step is slow.
//
// # Identity is never stored
//
// A Ref is derived from the manifest on every command (D7). kraai persists no
// resource ids: the lockfile records what was created so teardown can find it
// again, but the authoritative answer to "does this exist" is always a fresh
// lookup. How that lookup is performed varies per type, which is why a
// registration declares its Lookup strategy (D26) rather than the package
// assuming one rule holds everywhere.
package resource

import "context"

// Ref identifies one resource instance without asserting that it exists.
//
// Derived, not stored. Name is computed from the environment, service key and
// binding by internal/naming, so the same manifest always produces the same
// Ref and no state file is needed to answer "which resource is this".
type Ref struct {
	// Provider is the vendor fulfilling this resource, e.g. "cloudflare".
	Provider string
	// Type is the vendor's own resource type, e.g. "d1_database".
	Type string
	// Name is the derived resource name.
	Name string
	// Import is non-nil for an adopted resource the manifest points at by id
	// or name rather than one kraai created (D7). Its identity cannot be
	// derived, so it is carried explicitly.
	Import *Import
}

// Key is the registry key for this Ref's type: "provider/type".
func (r Ref) Key() string { return r.Provider + "/" + r.Type }

// Import is an explicit reference to a resource kraai did not create.
//
// Exactly one of ID or Name is set. A resource that already existed — created
// by hand, or by something else entirely — has no derivable identity, and the
// manifest is where that reference belongs rather than a separate state file.
type Import struct {
	ID   string
	Name string
}

// Spec is the desired state for one resource, taken from the manifest.
//
// Config stays opaque here because this package must not know what a D1
// database or a Neon branch is made of — each Resource decodes its own. The
// alternative, a union of every provider's fields, would put every vendor's
// vocabulary in the one package whose purpose is not having one.
type Spec struct {
	// Binding is the name the service refers to this resource by.
	Binding string
	// Config is the type-specific desired state.
	Config map[string]any
}

// State is what the provider actually holds for a resource.
type State struct {
	Ref Ref
	// ID is the provider-assigned identifier. Read fresh on every command and
	// recorded in the lockfile for teardown, never treated as the source of
	// truth for identity (D7).
	ID string
	// Attributes are the type-specific fields a later phase may need — a
	// connection host, a bucket name, a namespace id.
	Attributes map[string]any
}

// Resource is the contract every provisioned type implements (D15).
//
//go:generate go tool mockgen -source=resource.go -destination=mock_resource_test.go -package=resource
type Resource interface {
	// Get returns the resource's current state, or (nil, nil) when it does
	// not exist.
	//
	// Absence is an answer, not a failure. Teardown reads "not there" as
	// "already deleted, keep going" — a partial run must be retryable, and
	// every provider client in this repo already behaves this way. Returning
	// an error for absence would turn a clean retry into a failure and, worse,
	// would make a retried teardown look like it had failed when it had
	// succeeded.
	Get(ctx context.Context, ref Ref) (*State, error)

	// Create provisions the resource described by spec.
	Create(ctx context.Context, spec Spec) (*State, error)

	// Update reconciles an existing resource to spec.
	Update(ctx context.Context, ref Ref, spec Spec) (*State, error)

	// Delete removes the resource. Deleting something already gone is
	// success, for the same reason Get reports absence rather than failing.
	Delete(ctx context.Context, ref Ref) error
}

// LookupStrategy is how instances of a type are found (D26).
//
// Declared per type rather than assumed globally: when this was measured
// against a real provider, the derivable-name assumption held for only four
// of nine types. A single global rule cannot cover a real API surface.
type LookupStrategy string

const (
	// LookupByName means the derived name is the provider's own identifier,
	// so no lookup step is needed before a delete.
	LookupByName LookupStrategy = "byName"
	// LookupByAPI means the provider offers a native name lookup.
	LookupByAPI LookupStrategy = "byApi"
	// LookupByAttr means instances are listed and filtered on an attribute
	// the provider guarantees unique.
	LookupByAttr LookupStrategy = "byAttr"
	// LookupByTag means identity comes from a kraai-owned tag, for types with
	// no unique derivable attribute at all.
	//
	// A type using this must set its tag in the create call itself, never as
	// a follow-up write: a crash between the two orphans the resource
	// unfindably, which is the one failure no later run can clean up.
	LookupByTag LookupStrategy = "byTag"
)

// Valid reports whether s is a known strategy.
func (s LookupStrategy) Valid() bool {
	switch s {
	case LookupByName, LookupByAPI, LookupByAttr, LookupByTag:
		return true
	}
	return false
}
