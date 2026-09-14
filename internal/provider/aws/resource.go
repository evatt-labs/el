package aws

import (
	"context"
	"errors"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/resource"
)

// ErrNotImplemented is returned by Create, Update and Delete in this
// read-only slice.
//
// A silent stub — returning a zero State or a plain nil — would let a caller
// believe a mutation had happened when it had not, which is worse than
// refusing outright. The write path (async ProgressEvent polling, JSON Patch
// diffing, createOnlyProperties replacement detection) is real work left to
// its own workstream, not something to fake here.
var ErrNotImplemented = errors.New("aws: not implemented in the read-only slice yet")

// ccAPI is the Cloud Control surface this package's generic Resource needs,
// defined at this, its actual consumer (D21) — not the SDK's Input/Output
// shape, and not even *Client's own two methods' full generality, just what
// Get uses. A fake in resource_test.go implements two methods and needs
// neither an AWS account nor a network.
type ccAPI interface {
	// GetResource returns typeName/identifier's current properties, or
	// found=false when the resource does not exist. See Client.GetResource
	// for the absence-versus-failure contract this must preserve.
	GetResource(ctx context.Context, typeName, identifier string) (properties map[string]any, found bool, err error)
	// ListResources returns the primary identifier of every instance of
	// typeName.
	ListResources(ctx context.Context, typeName string) ([]string, error)
}

// matchFunc reports whether a resource's decoded properties are the one
// ref.Name identifies, for the byAttr and byTag lookup strategies. name is
// the derived resource name (D26's Ref.Name), not necessarily the value
// stored in the matched attribute directly — see cloudfrontMatch and
// apigatewayv2Match in identity.go for what each type actually compares.
type matchFunc func(properties map[string]any, name string) bool

// resourceType adapts one Cloud Control-backed AWS resource type to the
// resource contract (D15). One value of this type per registry entry in
// register.go; the CloudFormation TypeName and lookup strategy are what
// varies between AWS::S3::Bucket, AWS::Lambda::Function and the rest — the
// verbs themselves do not.
type resourceType struct {
	provider string
	typeName string
	lookup   resource.LookupStrategy
	client   ccAPI

	// match is nil for LookupByName, where ref.Name already is the primary
	// identifier and no candidate needs testing. Required for
	// LookupByAttr and LookupByTag.
	match matchFunc
}

// Get reports the resource's current state, or (nil, nil) when it does not
// exist.
//
// Absence is an answer, not a failure (D15): Client.GetResource already
// translates Cloud Control's ResourceNotFoundException this way, and every
// path below preserves it rather than collapsing a real error into the same
// return shape.
func (r *resourceType) Get(ctx context.Context, ref resource.Ref) (*resource.State, error) {
	identifier, properties, found, err := r.resolve(ctx, ref.Name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}

	// byAttr/byTag resolution already fetched the matching candidate's
	// properties while checking the match (see resolve) — reusing them here
	// avoids a second GetResource call for the identical identifier.
	if properties == nil {
		properties, found, err = r.client.GetResource(ctx, r.typeName, identifier)
		if err != nil {
			return nil, err
		}
		if !found {
			// Deleted between resolving its identifier and reading it.
			// Still an absence, not a second error path.
			return nil, nil
		}
	}

	return &resource.State{
		Ref:        resource.Ref{Provider: r.provider, Type: r.typeName, Name: ref.Name},
		ID:         identifier,
		Attributes: properties,
	}, nil
}

// resolve finds the Cloud Control primary identifier for name, per this
// type's lookup strategy (D26).
//
// For LookupByName, name already is the primary identifier — Cloud Control's
// own resource schema makes the derived name settable and unique at create
// time for every byName type this package registers (verified per type in
// register.go), so no lookup call precedes Get.
//
// For LookupByAttr and LookupByTag, this walks ListResources's identifiers
// and calls GetResource on each until match reports a hit or the list is
// exhausted. That is real cost — an N+1 call shape — accepted deliberately
// because ListResources does not reliably carry the attribute being matched
// (see Client.ListResources); calling GetResource per candidate is the only
// way to test against properties Cloud Control actually guarantees.
func (r *resourceType) resolve(ctx context.Context, name string) (identifier string, properties map[string]any, found bool, err error) {
	if r.lookup == resource.LookupByName {
		return name, nil, true, nil
	}

	candidates, err := r.client.ListResources(ctx, r.typeName)
	if err != nil {
		return "", nil, false, err
	}
	for _, candidate := range candidates {
		props, ok, err := r.client.GetResource(ctx, r.typeName, candidate)
		if err != nil {
			return "", nil, false, err
		}
		if !ok {
			// Listed, then gone by the time it was read. Not a match; try
			// the next candidate rather than failing the whole lookup.
			continue
		}
		if r.match(props, name) {
			return candidate, props, true, nil
		}
	}
	return "", nil, false, nil
}

// Create is not implemented in this read-only slice.
func (r *resourceType) Create(context.Context, resource.Spec) (*resource.State, error) {
	return nil, kerrors.Wrap(ErrNotImplemented, kerrors.CodeUnexpected, "cannot create %s/%s", r.provider, r.typeName)
}

// Update is not implemented in this read-only slice.
func (r *resourceType) Update(context.Context, resource.Ref, resource.Spec) (*resource.State, error) {
	return nil, kerrors.Wrap(ErrNotImplemented, kerrors.CodeUnexpected, "cannot update %s/%s", r.provider, r.typeName)
}

// Delete is not implemented in this read-only slice.
func (r *resourceType) Delete(context.Context, resource.Ref) error {
	return kerrors.Wrap(ErrNotImplemented, kerrors.CodeUnexpected, "cannot delete %s/%s", r.provider, r.typeName)
}
