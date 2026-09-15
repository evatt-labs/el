package aws

import (
	"github.com/evatt-labs/kraai/internal/manifest"
	"github.com/evatt-labs/kraai/internal/resource"
)

// Provider is the vendor name these types register under.
const Provider = "aws"

// Cloud Control TypeName constants for the resource types this package
// registers — AWS's own vocabulary, not kraai's, matching cfresource's
// precedent of using the vendor's type strings as Ref.Type.
const (
	TypeS3Bucket                      = "AWS::S3::Bucket"
	TypeLambdaFunction                = "AWS::Lambda::Function"
	TypeAPIGatewayV2API               = "AWS::ApiGatewayV2::Api"
	TypeCloudFrontDistribution        = "AWS::CloudFront::Distribution"
	TypeCertificateManagerCertificate = "AWS::CertificateManager::Certificate"
	TypeRoute53HostedZone             = "AWS::Route53::HostedZone"
	TypeRoute53RecordSet              = "AWS::Route53::RecordSet"
)

// Tier 2 compute types (aws-provider-compute): what it takes to actually run
// a deployed Lambda, beyond the function and its HTTP front door registered
// above. TypeArtifactBucket is this package's own registry vocabulary, not
// a real Cloud Control TypeName — see its own doc comment in
// artifactbucket.go for why.

// Register adds every type in this package to reg.
func Register(reg *resource.Registry, client *Client) error {
	for _, r := range Registrations(client) {
		if err := reg.Register(r); err != nil {
			return err
		}
	}
	return nil
}

// Registrations returns this package's registrations, exported so a caller
// can inspect or filter them before registering.
//
// # Why S3+HostedZone+Certificate+CloudFront+RecordSet share a capability,
// and so do Lambda+ApiGatewayV2
//
// Registry.Resolve returns every registration for a capability/provider pair
// together, in phase order — the mechanism neonresource already uses (D30)
// to expand one Postgres binding into a branch plus the Hyperdrive
// configuration fronting it. The same shape fits here: an "objects" binding
// on aws is the static-site stack in full — a DNS zone, a TLS certificate, a
// bucket, the CDN in front of it, and the DNS record pointing at that CDN —
// and a "compute" binding on aws is a function plus the HTTP API in front of
// it. Capability names come from internal/manifest's exported constants,
// never a new string — the vocabulary is exactly what the registry has
// implementations for.
//
// There is no DNS- or certificate-shaped capability in internal/manifest
// today, and this package does not invent one — adding a new capability
// constant is a manifest-package design decision (naming, documentation,
// every other provider's awareness of it) well outside this workstream's
// remit. CloudFront was already a stretch onto CapabilityObjects before this
// workstream; HostedZone, Certificate and RecordSet stretch it further onto
// the same capability for the same reason: they are the plumbing a static
// site's object storage needs to be reachable at all, not a distinct
// capability a manifest author chooses independently. Called out here
// rather than left implicit, and again in this workstream's PR description.
//
// This phase ordering does not fully solve the real cross-resource
// dependency a Route53-fronted, ACM-certified CloudFront site has: an ACM
// certificate normally needs a validation RecordSet in its hosted zone
// before Cloud Control will report it ISSUED, while the RecordSet aliasing
// the zone's apex to the CloudFront distribution needs the distribution to
// exist first — two different purposes for the same resource type at two
// different points in the sequence. The three-phase model (database,
// storage, compute) has no way to express "RecordSet before Certificate for
// validation, and also RecordSet after CloudFront for the alias" as two
// separate steps. This workstream registers RecordSet once, in PhaseCompute,
// for the common alias-to-CloudFront case, and surfaces the validation-order
// gap here for the applier or a manifest-level fix (e.g. DNS-validated
// certificates needing their own explicit ordering hint) to resolve.
func Registrations(client *Client) []resource.Registration {
	return []resource.Registration{
		{
			Provider: Provider, Type: TypeRoute53HostedZone,
			Capability: manifest.CapabilityObjects,
			// First: everything else in this stack — the certificate's
			// validation record, the CDN's alias record — is scoped inside
			// this zone.
			Phase: resource.PhaseStorage,
			// See hostedZoneMatch's doc comment: D26 calls this "byApi"
			// after Route53's native ListHostedZonesByName, but this
			// package's Cloud-Control-only engine resolves it via the same
			// list-and-match mechanism as LookupByAttr.
			Lookup: resource.LookupByAPI,
			Resource: &resourceType{
				provider: Provider, typeName: TypeRoute53HostedZone,
				lookup: resource.LookupByAPI, client: client, match: hostedZoneMatch,
			},
		},
		{
			Provider: Provider, Type: TypeCertificateManagerCertificate,
			Capability: manifest.CapabilityObjects,
			// Alongside storage: CloudFront (PhaseCompute) needs an issued
			// certificate to reference as its viewer certificate.
			Phase: resource.PhaseStorage,
			// DomainName is explicitly not unique (D26) — the same domain
			// can have multiple certificates outstanding during rotation —
			// so identity is a kraai-owned tag, stamped into the
			// CreateResource desired state itself (certificateStampTag).
			Lookup: resource.LookupByTag,
			Resource: &resourceType{
				provider: Provider, typeName: TypeCertificateManagerCertificate,
				lookup: resource.LookupByTag, client: client, match: certificateMatch, stampTag: certificateStampTag,
			},
		},
		{
			Provider: Provider, Type: TypeS3Bucket,
			Capability: manifest.CapabilityObjects,
			// Alongside storage: CloudFront's origin must exist before the
			// distribution fronting it does.
			Phase: resource.PhaseStorage,
			// BucketName is settable at create, globally unique, and is the
			// resource's own Ref/primary identifier (CloudFormation
			// TemplateReference, aws-resource-s3-bucket.html) — D7's
			// derivable-name assumption holds.
			Lookup:   resource.LookupByName,
			Resource: &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: client},
		},
		{
			Provider: Provider, Type: TypeCloudFrontDistribution,
			Capability: manifest.CapabilityObjects,
			// After storage: needs the bucket as its origin and the
			// certificate as its viewer certificate.
			Phase: resource.PhaseCompute,
			// AWS enforces alias uniqueness globally (D26). See
			// cloudfrontMatch's doc comment for the gap this leaves open —
			// a distribution with no alias cannot be found this way.
			Lookup: resource.LookupByAttr,
			Resource: &resourceType{
				provider: Provider, typeName: TypeCloudFrontDistribution,
				lookup: resource.LookupByAttr, client: client, match: cloudfrontMatch,
			},
		},
		{
			Provider: Provider, Type: TypeRoute53RecordSet,
			Capability: manifest.CapabilityObjects,
			// After CloudFront, for the common case this registers: an
			// alias record pointing the zone's name at the distribution.
			// See this function's own doc comment for the validation-record
			// ordering this does not solve.
			Phase: resource.PhaseCompute,
			// See recordSetMatch's doc comment: not byName, because
			// RecordSet's primary identifier is a compound
			// (HostedZoneId|Name|Type) this package's byName fast path has
			// no reliable way to construct without a lookup.
			Lookup: resource.LookupByAttr,
			Resource: &resourceType{
				provider: Provider, typeName: TypeRoute53RecordSet,
				lookup: resource.LookupByAttr, client: client, match: recordSetMatch,
			},
		},
		{
			Provider: Provider, Type: TypeLambdaFunction,
			Capability: manifest.CapabilityCompute,
			Phase:      resource.PhaseCompute,
			// No Triggers restriction: every service with AWS compute gets
			// a Lambda function regardless of how it's invoked — an HTTP
			// handler and a scheduled handler are both, in the end, a
			// function. What differs between them (the API Gateway/Url in
			// front, or the schedule rule behind it) is the other
			// registrations in this list.
			//
			// FunctionName is settable at create; CloudFormation marks it
			// "Update requires: Replacement", i.e. a createOnlyProperty and
			// this type's Ref (aws-resource-lambda-function.html) — D7's
			// derivable-name assumption holds.
			//
			// Resource is newLambdaFunctionResource, not a plain
			// resourceType: this is where a deployment package actually
			// gets built and uploaded (lambda.go) before Cloud Control ever
			// sees a Code property. See lambda.go's own doc comment.
			Lookup:   resource.LookupByName,
			Resource: newLambdaFunctionResource(client),
		},
		{
			Provider: Provider, Type: TypeArtifactBucket,
			Capability: manifest.CapabilityCompute,
			// Ahead of the function in PhaseCompute that uploads its
			// artifact there — phase-as-ordering, not phase-as-category,
			// the same technique and the same caveat PR #75 already
			// documented for ACM validation records and the
			// S3/CloudFront pair above: D12's two-level phase model has no
			// finer-grained dependency expression than "which phase," so
			// "before the function" is expressed by placing this in the
			// phase before PhaseCompute rather than by a real dependency
			// edge. See artifactbucket.go's own doc comment for the
			// further deviation this registration carries: one bucket per
			// service, not the brief's one bucket per environment.
			Phase: resource.PhaseStorage,
			// FunctionName-equivalent for a bucket is BucketName, settable
			// and unique at create (see TypeS3Bucket's own registration
			// above) — D7's derivable-name assumption holds here too; this
			// is byName against artifactBucketName's derived name, not
			// against ref.Name directly (see artifactBucketResource.Get).
			Lookup:   resource.LookupByName,
			Resource: newArtifactBucketResource(client),
		},
		{
			Provider: Provider, Type: TypeIAMRole,
			Capability: manifest.CapabilityCompute,
			// Ahead of the function that assumes it — see
			// TypeArtifactBucket's registration above for the identical
			// phase-as-ordering reasoning; this is the case the brief
			// itself names.
			Phase: resource.PhaseStorage,
			// RoleName is settable at create; IAM's own reference marks
			// renaming a role "Update requires: Replacement" — D7 holds.
			Lookup:   resource.LookupByName,
			Resource: newIAMRoleResource(client),
		},
		{
			Provider: Provider, Type: TypeSSMParameter,
			Capability: manifest.CapabilityCompute,
			// Same phase as the function whose artifact key it publishes;
			// no ordering dependency between the two (see ssmparameter.go:
			// each computes its own artifact key from the same pure input
			// rather than depending on the other's output), so PhaseCompute
			// is a category here, not an ordering device.
			Phase: resource.PhaseCompute,
			// Name is settable at create; SSM's own reference marks it
			// "Update requires: Replacement" — D7 holds, against the
			// derived parameter path (see ssmParamRef), not ref.Name
			// directly.
			Lookup:   resource.LookupByName,
			Resource: newSSMParameterResource(client),
		},
		{
			Provider: Provider, Type: TypeLambdaURL,
			Capability: manifest.CapabilityCompute,
			Phase:      resource.PhaseCompute,
			// HTTP-triggered services only — see lambdaurl.go's own doc
			// comment on why this coexists with ApiGatewayV2::Api under
			// the identical gate.
			Triggers: []string{manifest.TriggerHTTP},
			// Lambda::Url has no Tags property at all (verified against
			// its CloudFormation resource reference: AuthType, Cors,
			// InvokeMode, Qualifier, TargetFunctionArn only) — byTag is
			// unavailable here the way it is for ApiGatewayV2::Api. Its own
			// Name-equivalent identifier is TargetFunctionArn, which Lambda
			// itself guarantees at most one Function URL per function per
			// qualifier — a real uniqueness guarantee, so byAttr applies
			// (see lambdaURLMatch's own doc comment for the one part of
			// this that is not independently verified).
			Lookup:   resource.LookupByAttr,
			Resource: newLambdaURLResource(client),
		},
		{
			Provider: Provider, Type: TypeEventsRule,
			Capability: manifest.CapabilityCompute,
			Phase:      resource.PhaseCompute,
			// Schedule-triggered services only — the direct fix for the bug
			// that originally motivated Triggers: a schedule rule belongs
			// only to a service with a schedule expression to run, exactly
			// as ApiGatewayV2::Api/Lambda::Url belong only to one with an
			// HTTP surface.
			Triggers: []string{manifest.TriggerSchedule},
			// Name is settable at create; EventBridge's own reference marks
			// it "Update requires: Replacement" — D7 holds. See
			// eventsrule.go's own doc comment for why Events::Rule was
			// chosen over EventBridge Scheduler, and for the
			// AWS::Lambda::Permission gap this registration does not close.
			Lookup:   resource.LookupByName,
			Resource: newEventsRuleResource(client),
		},
		{
			Provider: Provider, Type: TypeAPIGatewayV2API,
			Capability: manifest.CapabilityCompute,
			Phase:      resource.PhaseCompute,
			// Triggers: only a service that declares itself HTTP-facing
			// gets an API Gateway. This is the per-service-compute
			// workstream's fix for the bug that motivated it: before
			// Triggers existed, every service using the compute capability
			// got both types this package registers under it, so a
			// schedule-invoked worker with no HTTP surface (kraai-api's
			// `tick`) planned an API Gateway nothing would ever call — not
			// merely redundant output, but a real, wrong resource once
			// `kraai apply` executes the plan. A service that declares no
			// compute: block at all still gets both, per
			// Registration.AppliesToTrigger's trigger=="" case — unchanged
			// from before this field existed.
			Triggers: []string{manifest.TriggerHTTP},
			// Not byName: Name is mutable ("Update requires: No
			// interruption" — not even createOnly) and AWS documents no
			// uniqueness constraint on it. See apigatewayv2Match's doc
			// comment for the full reasoning; this is a byTag type for the
			// same reason ACM::Certificate is under D26.
			Lookup: resource.LookupByTag,
			Resource: &resourceType{
				provider: Provider, typeName: TypeAPIGatewayV2API,
				lookup: resource.LookupByTag, client: client, match: apigatewayv2Match, stampTag: apigatewayv2StampTag,
			},
		},
	}
}
