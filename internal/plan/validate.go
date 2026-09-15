package plan

import "github.com/evatt-labs/kraai/internal/resource"

// SpecValidator is implemented by a resource.Resource that can reject a
// spec as invalid on its own terms — with no live state, no network call,
// nothing beyond the spec itself — before decide ever branches on whether
// the resource already exists.
//
// # The bug this exists to fix
//
// ImmutableDiffer (diff.go) is only ever consulted once Get has already
// found a live resource to compare against; on a brand-new environment
// every resource is ActionCreate and ImmutableDiffer is never reached at
// all. internal/provider/aws's decodeLambdaSettings used to be validated
// exactly that way — hung off lambdaFunctionResource.DiffersFromState — on
// the reasoning that DiffersFromState was "the one thing every compute
// service reaches unconditionally." That reasoning held for an
// already-existing environment and silently failed for a new one: a real
// `kraai plan` against a fresh environment with a typo'd
// `reservedConcurency` or an invalid `httpFrontDoor` value planned clean,
// no error, because decide's very first branch (state == nil ->
// ActionCreate) returns before DiffersFromState is ever type-asserted.
// The second case was worse than silent — an invalid httpFrontDoor made
// resource.Registration.SelectedBy match neither of the two mutually
// exclusive HTTP front-door registrations, so the plan was two resources
// short (API Gateway and its invoke permission) with nothing in the
// output saying why.
//
// SpecValidator is the fix: decide (planner.go) type-asserts it and calls
// ValidateSpec before Get runs at all, so every action — Create,
// NoChange, and Replace alike — gets the same validation pass regardless
// of whether the resource already exists.
//
// # Why an interface on Resource, not a check inside decide itself
//
// internal/plan does not, and must not, know a single vendor's settings
// vocabulary (region, reservedConcurrency, httpFrontDoor are all
// internal/provider/aws concepts) — the same reason ImmutableDiffer and
// resource.SecretProducer are optional interfaces satisfied structurally
// by a provider's own type rather than logic living in this package or
// resource.Resource's required method set. A resource type with nothing
// to validate (most of what this codebase registers today) simply does
// not implement SpecValidator, exactly as most types have no opinion on
// ImmutableDiffer.
//
// # Why before Get, not after
//
// Validation needs no live state — it is a property of the spec alone,
// the same reasoning ImmutableDiffer's own doc comment gives for why
// DiffersFromState is pure. Running it before Get means an invalid spec
// is reported without spending a live API call first: cheaper, and it
// means the ActionFailed a caller sees is unambiguously "your manifest is
// wrong," not entangled with whatever Get itself might also have failed
// on for an unrelated reason.
//
// # Why internal/plan calls it, not internal/assemble
//
// internal/assemble.Registry only ever sees providers.compute.settings —
// the provider-level settings, before manifest.MergeSettings layers a
// service's own Compute.Settings over it in internal/plan's expandCompute.
// A typo made entirely inside one service's own settings override would
// never reach assemble at all. decide is the one place that already holds
// the fully-merged, per-service Spec — the same one Get, and formerly only
// DiffersFromState, receive — so it is the only call site that can run
// this check against everything a manifest author actually wrote.
//
// # Read-only invariant preserved
//
// ValidateSpec takes a resource.Spec and returns only an error: no ref, no
// context, no path to Create/Update/Delete. it.res is still narrowed to
// getter (planner.go) before this type assertion runs, exactly as
// ImmutableDiffer's already does — SpecValidator adds a second optional
// capability decide may call, not a second way to reach a mutating verb.
type SpecValidator interface {
	// ValidateSpec reports whether spec is invalid on its own terms. It
	// must not perform I/O: decide calls it before Get, so a network call
	// here would run even for an already-failed comparison the caller
	// never gets to see, and would defeat the "cheaper than a live call"
	// reasoning above.
	ValidateSpec(spec resource.Spec) error
}
