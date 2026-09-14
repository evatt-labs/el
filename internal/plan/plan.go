package plan

import (
	"fmt"

	"github.com/evatt-labs/kraai/internal/resource"
)

// ActionKind is the outcome a plan proposes for one resource.
type ActionKind int

const (
	// ActionCreate means Get found nothing: the resource does not exist yet.
	ActionCreate ActionKind = iota
	// ActionNoChange means the resource exists and nothing about it would
	// change.
	ActionNoChange
	// ActionReplace means the resource exists but its desired spec differs
	// on a field the registered type cannot reconcile with Update — see
	// ImmutableDiffer in diff.go.
	ActionReplace
	// ActionFailed means Get itself failed: no outcome could be decided for
	// this resource. See Action.Err.
	ActionFailed
)

// String implements fmt.Stringer for readable output and error messages.
func (k ActionKind) String() string {
	switch k {
	case ActionCreate:
		return "create"
	case ActionNoChange:
		return "no-change"
	case ActionReplace:
		return "replace"
	case ActionFailed:
		return "failed"
	default:
		return fmt.Sprintf("ActionKind(%d)", int(k))
	}
}

// Item identifies what a single Action is about, independent of any live
// provider client — safe to render, log, or hand to a serializer on its
// own, unlike a resource.Registration, which carries a live resource.Resource.
type Item struct {
	// ServiceKey and Binding locate this resource in the manifest.
	ServiceKey string
	Binding    string
	// Capability is the manifest-level vocabulary this binding declared
	// ("postgres", "keyvalue", "objects", "queues").
	Capability string
	// Provider and Type are the vendor's own vocabulary for the concrete
	// resource type this binding (partly) expanded to (D30).
	Provider string
	Type     string
	// Phase is when this resource type is provisioned (D31).
	Phase resource.Phase
}

// Action is one resource type's planned outcome.
type Action struct {
	Item
	// Ref is this resource's derived identity (internal/naming).
	Ref resource.Ref
	// Spec is the desired state a subsequent apply would create or replace
	// this resource with. Its Secrets are always empty: a plan never
	// resolves a live credential, and wiring one in would require knowing
	// which other binding produces the value this one needs — vendor-
	// specific knowledge this package deliberately does not have. That
	// wiring belongs to whatever builds apply next, using the same
	// resource.Outputs/resource.SecretProducer machinery the resource
	// contract already defines for it.
	Spec resource.Spec
	// Current is what Get found, or nil when the resource does not exist
	// (ActionCreate) or Get failed (ActionFailed).
	Current *resource.State
	// Kind is the proposed outcome.
	Kind ActionKind
	// Err is set if and only if Kind is ActionFailed.
	Err error
}

// Plan is the ordered result of walking a manifest: what would happen to
// every resource type every declared binding expands to, in provisioning
// order (D31, stable within a phase).
type Plan struct {
	Actions []Action
}

// HasChanges reports whether applying this plan would create or replace
// anything.
func (p *Plan) HasChanges() bool {
	for _, a := range p.Actions {
		if a.Kind == ActionCreate || a.Kind == ActionReplace {
			return true
		}
	}
	return false
}

// HasFailures reports whether any resource's current state could not be
// read, meaning this plan is incomplete — see the package doc's "Partial
// failure" section.
func (p *Plan) HasFailures() bool {
	for _, a := range p.Actions {
		if a.Kind == ActionFailed {
			return true
		}
	}
	return false
}
