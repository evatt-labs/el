package plan

import "github.com/evatt-labs/kraai/internal/resource"

// ImmutableDiffer is implemented by a resource.Resource whose Spec carries
// fields that cannot be reconciled by Update — which, per
// resource.ErrImmutable, is every type registered in this codebase today.
//
// Optional, for the same reason resource.SecretProducer is (see
// internal/resource/immutable.go): most of today's registered types have
// no meaningfully-configurable field to compare at all. Their derived name
// (D22) is their entire identity, and Get finds them by that same name, so
// an existing match has already agreed with the desired spec on everything
// it carries — there is nothing left to differ, and a type that has
// nothing to say here correctly leaves an existing match as ActionNoChange
// rather than being forced to implement a comparison against fields it
// does not have.
//
// A type whose Config does encode something immutable — a storage class, a
// region, an engine version — implements this to report a real
// disagreement. It is not a method on resource.Resource itself: adding it
// there would force every current and future type to answer a question
// most of them have no content for.
type ImmutableDiffer interface {
	// DiffersFromState reports whether spec disagrees with state on a field
	// that cannot be changed in place — the same test that, if true, would
	// make a real Update call return resource.ErrImmutable.
	DiffersFromState(spec resource.Spec, state *resource.State) (bool, error)
}
