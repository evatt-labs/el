package aws

import (
	"context"
	"reflect"
	"sync"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/resource"
)

// ccAPI is the Cloud Control surface this package's generic Resource needs,
// defined at this, its actual consumer (D21) — not the SDK's Input/Output
// shape. A fake in resource_test.go implements it and needs neither an AWS
// account nor a network.
type ccAPI interface {
	// GetResource returns typeName/identifier's current properties, or
	// found=false when the resource does not exist. See Client.GetResource
	// for the absence-versus-failure contract this must preserve.
	GetResource(ctx context.Context, typeName, identifier string) (properties map[string]any, found bool, err error)
	// ListResources returns the primary identifier of every instance of
	// typeName.
	ListResources(ctx context.Context, typeName string) ([]string, error)
	// CreateResource submits desiredState and polls to a terminal state,
	// returning the provider-assigned identifier and resulting properties.
	CreateResource(ctx context.Context, typeName string, desiredState map[string]any) (identifier string, properties map[string]any, err error)
	// UpdateResource submits an RFC 6902 JSON Patch document against
	// identifier and polls to a terminal state, returning the resulting
	// properties.
	UpdateResource(ctx context.Context, typeName, identifier string, patch []byte) (properties map[string]any, err error)
	// DeleteResource submits a delete and polls to a terminal state.
	// Deleting something already absent is success; see Client.DeleteResource.
	DeleteResource(ctx context.Context, typeName, identifier string) error
	// DescribeType fetches and decodes typeName's CloudFormation resource
	// provider schema.
	DescribeType(ctx context.Context, typeName string) (Schema, error)
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
	// LookupByAttr, LookupByTag and LookupByAPI (see hostedZoneMatch's doc
	// comment for why byApi uses the same match mechanism as byAttr here).
	match matchFunc

	// stampTag is required exactly when lookup is LookupByTag: it writes
	// this type's identity tag into the CreateResource desired state (D26).
	// Kept separate from match, rather than deriving one from the other,
	// because the two run against different shapes — stampTag builds
	// desired state going out, match reads properties coming back — and
	// because not every byTag type spells "Tags" the same way (see
	// identity.go's array-shaped versus flat-map-shaped Tags).
	stampTag stampFunc

	// schemaMu, schema and schemaLoaded cache this type's CloudFormation
	// resource-provider schema for the process's lifetime (Client.DescribeType's
	// own doc comment explains why caching, not vendoring, is the right
	// amount of work here). Every Update and DiffersFromState call needs
	// this schema; fetching it once per type rather than once per call
	// avoids turning every write-path call into two API round trips.
	//
	// A mutex guarding a plain bool rather than sync.Once: a phase's
	// resources run concurrently under errgroup.SetLimit (D13), and more
	// than one resource of the same type can be updated in the same phase,
	// so this is genuinely reachable from multiple goroutines. sync.Once
	// would also cache a transient failure (a single throttled
	// DescribeType) forever for the rest of the run; caching only on
	// success means a later retry can still succeed.
	schemaMu     sync.Mutex
	schema       Schema
	schemaLoaded bool
}

// getSchema returns this type's cached CloudFormation resource-provider
// schema, fetching it on first use. A failed fetch is not cached, so the
// next call retries rather than being stuck on one transient error for the
// rest of the process.
func (r *resourceType) getSchema(ctx context.Context) (Schema, error) {
	r.schemaMu.Lock()
	defer r.schemaMu.Unlock()
	if r.schemaLoaded {
		return r.schema, nil
	}
	schema, err := r.client.DescribeType(ctx, r.typeName)
	if err != nil {
		return Schema{}, err
	}
	r.schema = schema
	r.schemaLoaded = true
	return r.schema, nil
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

// Create provisions the resource from spec, submitting spec.Config as Cloud
// Control's desired state and polling the resulting ProgressEvent to a
// terminal state.
func (r *resourceType) Create(ctx context.Context, spec resource.Spec) (*resource.State, error) {
	if spec.Name == "" {
		return nil, kerrors.Validation(
			"cannot create %s for binding %q without a derived name", r.typeName, spec.Binding)
	}

	desired := make(map[string]any, len(spec.Config)+1)
	for k, v := range spec.Config {
		desired[k] = v
	}

	if r.lookup == resource.LookupByTag {
		if r.stampTag == nil {
			return nil, kerrors.Validation(
				"%s is registered LookupByTag but declares no stampTag function", r.typeName)
		}
		// The identity tag rides in this same CreateResource call (D26):
		// writing it as a follow-up call would leave a window where a crash
		// between create and tag orphans the resource unfindably — the one
		// failure no later run could clean up.
		r.stampTag(desired, spec.Name)
	}

	identifier, properties, err := r.client.CreateResource(ctx, r.typeName, desired)
	if err != nil {
		return nil, err
	}
	return &resource.State{
		Ref:        resource.Ref{Provider: r.provider, Type: r.typeName, Name: spec.Name},
		ID:         identifier,
		Attributes: properties,
	}, nil
}

// Update reconciles an existing resource to spec, or refuses with
// resource.ErrImmutable when this type's schema has no update handler
// (IMMUTABLE provisioning: create/read/delete only).
//
// The current state is fetched here rather than accepted as a parameter —
// the Resource contract's Update signature is (ctx, ref, spec), matching
// every other provider in this repo — so the diff Update needs is built
// from a fresh Get rather than state the caller might be holding stale.
func (r *resourceType) Update(ctx context.Context, ref resource.Ref, spec resource.Spec) (*resource.State, error) {
	schema, err := r.getSchema(ctx)
	if err != nil {
		return nil, err
	}
	if !schema.HasUpdateHandler() {
		return nil, kerrors.Wrap(resource.ErrImmutable, kerrors.CodeValidation,
			"%s has no update handler; a differing property requires replacement, not an update", r.typeName)
	}

	identifier, properties, found, err := r.resolve(ctx, ref.Name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, kerrors.Validation("cannot update %s %q: it does not currently exist", r.typeName, ref.Name)
	}
	if properties == nil {
		// byName resolve() returns no properties (it has no reason to fetch
		// them) — Get one fresh set to diff against, same as Get itself does.
		properties, found, err = r.client.GetResource(ctx, r.typeName, identifier)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, kerrors.Validation("cannot update %s %q: it was deleted concurrently", r.typeName, ref.Name)
		}
	}

	patch, err := buildPatch(properties, spec.Config)
	if err != nil {
		return nil, err
	}
	if len(patch) == 0 || string(patch) == "[]" {
		// Nothing in spec.Config differs from the live properties. Returning
		// the current state rather than special-casing "no-op" keeps this
		// method's return shape identical whether or not anything changed,
		// and avoids a pointless UpdateResource round trip.
		return &resource.State{
			Ref:        resource.Ref{Provider: r.provider, Type: r.typeName, Name: ref.Name},
			ID:         identifier,
			Attributes: properties,
		}, nil
	}

	updated, err := r.client.UpdateResource(ctx, r.typeName, identifier, patch)
	if err != nil {
		return nil, err
	}
	return &resource.State{
		Ref:        resource.Ref{Provider: r.provider, Type: r.typeName, Name: ref.Name},
		ID:         identifier,
		Attributes: updated,
	}, nil
}

// Delete removes the resource, treating one already absent as success —
// both when resolve finds no identifier at all, and when Cloud Control
// itself reports the resource gone (Client.DeleteResource's own contract).
func (r *resourceType) Delete(ctx context.Context, ref resource.Ref) error {
	identifier, _, found, err := r.resolve(ctx, ref.Name)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return r.client.DeleteResource(ctx, r.typeName, identifier)
}

// DiffersFromState reports whether spec disagrees with state on one of this
// type's createOnlyProperties, implementing plan.ImmutableDiffer
// structurally: this package does not import internal/plan (out of scope
// for this workstream, and plan is being built in parallel), it merely
// satisfies plan's interface by having a method of the matching name and
// signature, which is how plan's own type assertion finds it.
//
// # Why context.Background() here, and only here
//
// Every other network call in this package takes the caller's context.
// plan.ImmutableDiffer's signature — defined by another workstream, out of
// scope to change here — carries no context parameter at all, so the schema
// fetch this method needs has no caller context to propagate. In practice
// this is low-risk: getSchema's own cache means the fetch only actually
// happens once per type per process, and a real DescribeType call is small
// and fast relative to the async operations this package's other calls
// wait on. Flagged in this workstream's PR description as something the
// planner workstream may want to reconsider — a context-carrying
// ImmutableDiffer would let this propagate cancellation like everything
// else here.
func (r *resourceType) DiffersFromState(spec resource.Spec, state *resource.State) (bool, error) {
	schema, err := r.getSchema(context.Background())
	if err != nil {
		return false, err
	}

	for _, pointer := range schema.CreateOnlyProperties {
		path := schemaPropertyPath(pointer)
		if len(path) == 0 {
			continue
		}

		desiredVal, hasDesired := lookupPath(spec.Config, path)
		if !hasDesired {
			// Not declared in the manifest at all. D6: a property the
			// manifest never mentions is never touched, so its absence here
			// can never itself be the source of a disagreement.
			continue
		}
		desiredNorm, err := normalizeForCompare(desiredVal)
		if err != nil {
			return false, kerrors.Wrap(err, kerrors.CodeUnexpected, "normalizing desired %s for %s", pointer, r.typeName)
		}

		currentVal, hasCurrent := lookupPath(state.Attributes, path)
		if !hasCurrent {
			// Declared in the manifest but Cloud Control never reported it —
			// treat as a real difference rather than assuming a match it
			// cannot support evidence for.
			return true, nil
		}
		currentNorm, err := normalizeForCompare(currentVal)
		if err != nil {
			return false, kerrors.Wrap(err, kerrors.CodeUnexpected, "normalizing current %s for %s", pointer, r.typeName)
		}

		if !reflect.DeepEqual(desiredNorm, currentNorm) {
			return true, nil
		}
	}
	return false, nil
}
