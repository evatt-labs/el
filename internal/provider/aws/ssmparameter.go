package aws

import (
	"context"

	"github.com/evatt-labs/kraai/internal/resource"
)

// TypeSSMParameter is AWS::SSM::Parameter's Cloud Control TypeName.
const TypeSSMParameter = "AWS::SSM::Parameter"

// artifactParameterPath derives the SSM Parameter Store path this package
// publishes a service's currently-deployed artifact key under.
//
// A "/"-rooted path rather than a flat name: Parameter Store treats a
// leading "/" as a hierarchy, which is what lets kraai.dev's own tooling
// (or an operator's `aws ssm get-parameters-by-path /kraai/`) enumerate
// every service's current artifact in one call instead of needing to
// already know each service's name.
func artifactParameterPath(serviceName string) string {
	return "/kraai/" + serviceName + "/artifact-key"
}

// ssmParameterResource publishes a service's currently-deployed Lambda
// artifact's S3 key as a discoverable SSM Parameter.
//
// # Why this exists
//
// Nothing else kraai writes records which artifact is actually live for a
// service without calling Lambda's own GetFunction API (D6: no state
// document). A Standard String parameter at a well-known path is a cheap,
// AWS-native way for operator tooling — a dashboard, a rollback script, a
// CI check — to answer "what code is running" without kraai inventing its
// own state format or that tooling needing Lambda-specific permissions at
// all, only ssm:GetParameter. It is genuinely one of this workstream's own
// artifacts, not a bolt-on: the key it publishes is the exact deterministic
// key artifact.go computes for the same service's Lambda function (see
// lambda.go).
type ssmParameterResource struct {
	inner *resourceType
}

func newSSMParameterResource(client *Client) *ssmParameterResource {
	return &ssmParameterResource{
		inner: &resourceType{provider: Provider, typeName: TypeSSMParameter, lookup: resource.LookupByName, client: client},
	}
}

// translate computes the artifact key fresh from spec.Config["dir"] — the
// same deterministic, side-effect-free zip build lambda.go's own
// DiffersFromState relies on being safe during `kraai plan` (see that
// type's doc comment). Recomputed here rather than shared with the Lambda
// function's own translation: the two are independent registrations with
// no ordering guarantee relative to each other within PhaseCompute, so
// each computes its own answer from the one shared, pure input (dir) they
// both read, rather than one depending on values the other produced.
func (p *ssmParameterResource) translate(spec resource.Spec) (resource.Spec, error) {
	dir, _ := spec.Config["dir"].(string)
	_, sha256Hex, err := buildArtifact(dir)
	if err != nil {
		return resource.Spec{}, err
	}

	translated := spec
	translated.Config = map[string]any{
		"Name":  artifactParameterPath(spec.Name),
		"Type":  "String",
		"Value": artifactObjectKey(spec.Name, sha256Hex),
	}
	return translated, nil
}

func (p *ssmParameterResource) Get(ctx context.Context, ref resource.Ref) (*resource.State, error) {
	state, err := p.inner.Get(ctx, ssmParamRef(ref))
	if err != nil || state == nil {
		return state, err
	}
	// Report under the caller's own Ref (the service name), not the
	// derived parameter path — see artifactbucket.go's Get for the
	// identical reasoning.
	state.Ref = ref
	return state, nil
}

func (p *ssmParameterResource) Create(ctx context.Context, spec resource.Spec) (*resource.State, error) {
	translated, err := p.translate(spec)
	if err != nil {
		return nil, err
	}
	realSpec := translated
	realSpec.Name = artifactParameterPath(spec.Name)
	state, err := p.inner.Create(ctx, realSpec)
	if err != nil || state == nil {
		return state, err
	}
	state.Ref.Name = spec.Name
	return state, nil
}

func (p *ssmParameterResource) Update(ctx context.Context, ref resource.Ref, spec resource.Spec) (*resource.State, error) {
	translated, err := p.translate(spec)
	if err != nil {
		return nil, err
	}
	return p.inner.Update(ctx, ssmParamRef(ref), translated)
}

func (p *ssmParameterResource) Delete(ctx context.Context, ref resource.Ref) error {
	return p.inner.Delete(ctx, ssmParamRef(ref))
}

// DiffersFromState delegates to the generic engine's schema-driven check.
// Name is SSM::Parameter's createOnlyProperty (renaming a parameter means a
// new one); Value and Type are updatable in place, so a changed artifact
// key alone never forces a replacement here — it flows through Update.
func (p *ssmParameterResource) DiffersFromState(spec resource.Spec, state *resource.State) (bool, error) {
	translated, err := p.translate(spec)
	if err != nil {
		return false, err
	}
	return p.inner.DiffersFromState(translated, state)
}

// ssmParamRef rewrites the caller's Ref.Name (the service-derived name)
// into the real parameter path Get/Update/Delete address, mirroring
// artifactbucket.go's bucketRef for the identical reason: this type's
// externally-visible Ref stays the service name every other Tier 2
// registration for the same service shares, while the actual AWS-side
// identifier is a derived path.
func ssmParamRef(ref resource.Ref) resource.Ref {
	return resource.Ref{Provider: ref.Provider, Type: ref.Type, Name: artifactParameterPath(ref.Name)}
}
