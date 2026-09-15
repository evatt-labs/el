package aws

import (
	"context"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/resource"
)

// TypeArtifactBucket is this registry's key for the per-service Lambda
// artifact bucket — deliberately not "AWS::S3::Bucket" itself.
//
// Registry.Register keys uniqueness on Provider+Type alone (D15's registry,
// internal/resource/registry.go), with no notion of "the same AWS type
// registered twice for two different capabilities." "AWS::S3::Bucket" is
// already registered under CapabilityObjects for a manifest's own
// `objects:` bindings (see TypeS3Bucket above); a manifest that configures
// both objects and compute on aws — kraai's own kraai-api is exactly this
// shape — would fail Register outright on a duplicate key if this
// registration reused that same string. Registration.Type is this
// package's own registry vocabulary, not required to equal the Cloud
// Control TypeName it drives (that's resourceType.typeName, a separate
// field) — every other registration in this package keeps the two equal
// out of clarity, per register.go's own "AWS's own vocabulary, not
// kraai's" comment, but nothing enforces that, and this is the one type
// here that genuinely needs to diverge from it rather than collide.
const TypeArtifactBucket = "AWS::S3::Bucket::ArtifactBucket"

// maxBucketNameLen is S3's own bucket name length ceiling.
const maxBucketNameLen = 63

// artifactBucketSuffix disambiguates the artifact bucket's real S3 name
// from the derived service name every other Tier 2 registration for the
// same service reuses verbatim (the Lambda function, its execution role,
// its Url) — appending it is what keeps this bucket from colliding with
// those other resources' own AWS-side names, which is otherwise
// unenforced: nothing stops an AWS::IAM::Role and an AWS::Lambda::Function
// sharing a name (they are different namespaces), but two S3 buckets never
// can, globally, across every AWS account on Earth.
const artifactBucketSuffix = "-artifacts"

// artifactBucketName derives the real S3 bucket name from a service's
// kraai-derived name (naming.ServiceName's output, already lowercase and
// limited to [a-z0-9-] — see that function's own doc comment), truncating
// to S3's 63-character ceiling the same way internal/naming already
// truncates for the identical DNS-compliance reason (R2/S3 both cap bucket
// names at 63).
func artifactBucketName(serviceName string) string {
	name := serviceName + artifactBucketSuffix
	if len(name) <= maxBucketNameLen {
		return name
	}
	return strings.TrimRight(name[:maxBucketNameLen], "-")
}

// artifactBucketResource provisions the per-service S3 bucket that holds a
// Lambda's deployment artifacts.
//
// # Per-service, not per-environment — a deviation from the brief, flagged here
//
// The brief calls for one artifact bucket shared by every service in an
// environment. Structurally, that shape is not reachable from this
// workstream alone: internal/plan's expandCompute (out of scope for this
// workstream, D36) expands every CapabilityCompute registration once per
// service, deriving each Ref from that service's own name, with no
// environment-only expansion point this package's registration can hook
// into. But even setting that aside, a genuinely shared bucket is actively
// wrong under this design's own concurrency model, not merely
// unreachable: `kraai plan` calls Get for every planned item in a phase
// concurrently (D13), before anything is created. If every service's
// artifact-bucket item resolved to the identical AWS-side bucket name, a
// fresh environment's first `kraai plan` would have every service's Get
// independently observe "does not exist yet" and plan ActionCreate — none
// of them would see a sibling's not-yet-applied plan. `kraai apply` then
// runs every ActionCreate in the phase concurrently too, which means N
// services racing N concurrent CreateResource calls at the same bucket
// name, a create pattern this contract's Resource.Create was never built
// to tolerate (it is called once per Ref, not N times concurrently for
// equivalent Refs). One bucket per service sidesteps this entirely: each
// service's Ref is genuinely distinct, so there is only ever one creator
// per bucket. Still created in PhaseStorage ahead of the function that
// reads it, still destroyed whenever that service's own resources are
// torn down, but multiplied by service count rather than shared. A future
// workstream wanting a genuinely shared bucket needs a real
// environment-scoped expansion phase in internal/plan plus a way to
// serialize (or dedupe) concurrent creators of the same resource — neither
// exists today, and inventing either here is out of this workstream's
// scope; noted in this workstream's PR description rather than worked
// around silently.
type artifactBucketResource struct {
	inner *resourceType
}

func newArtifactBucketResource(client *Client) *artifactBucketResource {
	return &artifactBucketResource{
		inner: &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: client},
	}
}

// bucketRef/bucketSpec rewrite a compute Ref/Spec's derived service name
// into this type's own real S3 bucket name before delegating to the
// generic byName engine — see artifactBucketName's own doc comment.

func bucketRef(ref resource.Ref) resource.Ref {
	return resource.Ref{Provider: ref.Provider, Type: ref.Type, Name: artifactBucketName(ref.Name)}
}

func (a *artifactBucketResource) Get(ctx context.Context, ref resource.Ref) (*resource.State, error) {
	state, err := a.inner.Get(ctx, bucketRef(ref))
	if err != nil || state == nil {
		return state, err
	}
	// Report the state under the caller's own Ref (the service-derived
	// name), not the transformed bucket name: everything above this type
	// (plan, the lockfile) identifies this resource by the Ref the
	// registry handed it, and substituting a different Ref.Name here would
	// make this resource's identity inconsistent with every other Tier 2
	// registration for the same service.
	state.Ref = ref
	return state, nil
}

func (a *artifactBucketResource) Create(ctx context.Context, spec resource.Spec) (*resource.State, error) {
	realSpec := spec
	realSpec.Name = artifactBucketName(spec.Name)
	state, err := a.inner.Create(ctx, realSpec)
	if err != nil || state == nil {
		return state, err
	}
	state.Ref.Name = spec.Name
	return state, nil
}

// Update is refused: this bucket has no configuration a manifest declares
// or a service needs changed in place — its only job is to exist and hold
// objects Lambda's Create/Update paths upload directly via S3 PutObject
// (client.go), outside Cloud Control entirely. A difference here would
// only ever be this bucket's own name changing, which Delete-then-Create
// already handles as a replacement.
func (a *artifactBucketResource) Update(context.Context, resource.Ref, resource.Spec) (*resource.State, error) {
	return nil, kerrors.Wrap(resource.ErrImmutable, kerrors.CodeValidation,
		"an artifact bucket has no in-place configuration; replace it instead")
}

func (a *artifactBucketResource) Delete(ctx context.Context, ref resource.Ref) error {
	return a.inner.Delete(ctx, bucketRef(ref))
}
