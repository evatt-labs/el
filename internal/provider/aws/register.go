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
			// FunctionName is settable at create; CloudFormation marks it
			// "Update requires: Replacement", i.e. a createOnlyProperty and
			// this type's Ref (aws-resource-lambda-function.html) — D7's
			// derivable-name assumption holds.
			Lookup:   resource.LookupByName,
			Resource: &resourceType{provider: Provider, typeName: TypeLambdaFunction, lookup: resource.LookupByName, client: client},
		},
		{
			Provider: Provider, Type: TypeAPIGatewayV2API,
			Capability: manifest.CapabilityCompute,
			Phase:      resource.PhaseCompute,
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
