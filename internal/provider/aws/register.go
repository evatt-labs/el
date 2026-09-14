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
	TypeS3Bucket               = "AWS::S3::Bucket"
	TypeLambdaFunction         = "AWS::Lambda::Function"
	TypeAPIGatewayV2API        = "AWS::ApiGatewayV2::Api"
	TypeCloudFrontDistribution = "AWS::CloudFront::Distribution"
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
// # Why S3+CloudFront share a capability, and so do Lambda+ApiGatewayV2
//
// Registry.Resolve returns every registration for a capability/provider pair
// together, in phase order — the mechanism neonresource already uses (D30)
// to expand one Postgres binding into a branch plus the Hyperdrive
// configuration fronting it. The same shape fits here: an "objects" binding
// on aws is a bucket plus the CDN in front of it, and a "compute" binding on
// aws is a function plus the HTTP API in front of it. Capability names come
// from internal/manifest's exported constants, never a new string — the
// vocabulary is exactly what the registry has implementations for.
//
// There is no fifth, CDN-shaped capability in internal/manifest today, and
// this package does not invent one. CloudFront is a genuine stretch onto
// CapabilityObjects rather than an obvious fit; it is called out here rather
// than left implicit.
func Registrations(client *Client) []resource.Registration {
	return []resource.Registration{
		{
			Provider: Provider, Type: TypeS3Bucket,
			Capability: manifest.CapabilityObjects,
			// First: CloudFront's origin must exist before the distribution
			// fronting it does.
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
			// After storage, which its origin depends on.
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
				lookup: resource.LookupByTag, client: client, match: apigatewayv2Match,
			},
		},
	}
}
