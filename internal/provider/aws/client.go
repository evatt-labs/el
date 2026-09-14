package aws

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// maxListPages bounds a ListResources page walk, as a backstop against a
// NextToken that never stops advancing — the same defensive pattern the
// Neon client uses for its cursor walk.
const maxListPages = 100

// cloudControlAPI is the subset of *cloudcontrol.Client this package's
// *Client calls. Narrowed to exactly GetResource and ListResources — nothing
// about CreateResource, UpdateResource, DeleteResource or the async
// ProgressEvent operations belongs here, because this slice never calls
// them.
//
// Shaped like the SDK's own method signatures rather than this package's
// vocabulary, unlike ccAPI in resource.go: this is the seam being
// substituted in client_test.go, and the thing on the other side of it is
// the SDK client itself, which is a concrete struct with no interface of
// its own to depend on instead.
type cloudControlAPI interface {
	GetResource(ctx context.Context, params *cloudcontrol.GetResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error)
	ListResources(ctx context.Context, params *cloudcontrol.ListResourcesInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error)
}

// cloudFormationAPI is the subset of *cloudformation.Client this package
// calls: DescribeType, to fetch a resource type's schema.
type cloudFormationAPI interface {
	DescribeType(ctx context.Context, params *cloudformation.DescribeTypeInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeTypeOutput, error)
}

// Client is a thin Cloud Control + CloudFormation client.
//
// "Thin" here means its exported methods already speak this package's own
// vocabulary — a decoded properties map, a plain identifier list — rather
// than the SDK's Input/Output types. resource.go's ccAPI interface is
// satisfied by *Client precisely because of that shape, so nothing above
// this file needs to know the SDK exists.
type Client struct {
	cc cloudControlAPI
	cf cloudFormationAPI
}

// Option configures a Client.
type Option func(*Client)

// WithCloudControlAPI substitutes the Cloud Control caller, which is how
// tests exercise Client without an AWS account or network (D21).
func WithCloudControlAPI(api cloudControlAPI) Option {
	return func(c *Client) { c.cc = api }
}

// WithCloudFormationAPI substitutes the CloudFormation caller.
func WithCloudFormationAPI(api cloudFormationAPI) Option {
	return func(c *Client) { c.cf = api }
}

// New builds a Client for settings.Region, authenticating via the AWS SDK's
// own default credential chain (environment, shared config, IMDS — kraai
// reads no AWS credential itself, per D18's "external touchpoints own their
// own auth").
func New(ctx context.Context, settings Settings, opts ...Option) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(settings.Region))
	if err != nil {
		// LoadDefaultConfig's error can name a credential file path but never
		// a credential value, so wrapping it is safe.
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "loading AWS configuration for region %q", settings.Region)
	}

	c := &Client{
		cc: cloudcontrol.NewFromConfig(cfg),
		cf: cloudformation.NewFromConfig(cfg),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// GetResource returns typeName/identifier's current properties, or
// found=false when Cloud Control reports the resource does not exist.
//
// # Absence versus failure
//
// ResourceNotFoundException is the only outcome translated to found=false,
// err=nil. Every other error — throttling, access denied, a network
// failure, a malformed response — is returned as a real error with
// found=false and must not be read as absence: Get's caller (and, through
// it, teardown) treats "does not exist" as "already deleted, keep going",
// so misreading an outage as absence would orphan a real resource.
func (c *Client) GetResource(ctx context.Context, typeName, identifier string) (map[string]any, bool, error) {
	out, err := c.cc.GetResource(ctx, &cloudcontrol.GetResourceInput{
		TypeName:   aws.String(typeName),
		Identifier: aws.String(identifier),
	})
	if err != nil {
		var notFound *cctypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil, false, nil
		}
		return nil, false, kerrors.Wrap(err, kerrors.CodeUnexpected, "getting %s %q", typeName, identifier)
	}
	if out.ResourceDescription == nil || out.ResourceDescription.Properties == nil {
		return nil, false, kerrors.Validation("GetResource for %s %q returned no properties", typeName, identifier)
	}

	var properties map[string]any
	if err := json.Unmarshal([]byte(*out.ResourceDescription.Properties), &properties); err != nil {
		return nil, false, kerrors.Wrap(err, kerrors.CodeUnexpected, "decoding properties for %s %q", typeName, identifier)
	}
	return properties, true, nil
}

// ListResources returns the primary identifier of every instance of
// typeName Cloud Control can see, across all pages.
//
// Deliberately returns identifiers only, never properties. Cloud Control's
// own documentation guarantees only the identifier per resource — "it may
// include part or all of the resource's properties" — and its own worked
// example shows a type (Kinesis streams) whose list response carries a
// single property while everything else requires GetResource. Exposing a
// partial, type-dependent properties map here would invite exactly the bug
// this package's byAttr/byTag lookups must not have: trusting a field that
// List never promised to populate.
func (c *Client) ListResources(ctx context.Context, typeName string) ([]string, error) {
	var identifiers []string
	var nextToken *string

	for page := 0; page < maxListPages; page++ {
		out, err := c.cc.ListResources(ctx, &cloudcontrol.ListResourcesInput{
			TypeName:  aws.String(typeName),
			NextToken: nextToken,
		})
		if err != nil {
			var notFound *cctypes.TypeNotFoundException
			if errors.As(err, &notFound) {
				return nil, kerrors.Wrap(err, kerrors.CodeValidation, "AWS Cloud Control has no registered type %q", typeName)
			}
			return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "listing %s", typeName)
		}
		for _, desc := range out.ResourceDescriptions {
			if desc.Identifier != nil {
				identifiers = append(identifiers, *desc.Identifier)
			}
		}
		if out.NextToken == nil || *out.NextToken == "" {
			return identifiers, nil
		}
		nextToken = out.NextToken
	}
	return nil, kerrors.Validation("listing %s did not terminate within %d pages", typeName, maxListPages)
}

// Schema is the subset of a CloudFormation resource-provider schema this
// package currently decodes.
//
// DescribeType's Schema field is the full resource-provider schema as a JSON
// string — up to 116KB for AWS::CloudFront::Distribution, per the
// aws-provider-core workstream's own measurement — carrying properties,
// required, handlers and more. Only the fields relevant to identity and
// replacement detection are decoded here; the rest is left for the
// write-path workstream that actually needs schema-driven diffing.
type Schema struct {
	// PrimaryIdentifier is the property path (or paths, for a compound
	// identifier) Cloud Control treats as this type's primary identifier.
	PrimaryIdentifier []string `json:"primaryIdentifier"`
	// CreateOnlyProperties are the properties that force a replacement
	// rather than an in-place update — the authoritative source for a
	// future plan's replace-or-update decision.
	CreateOnlyProperties []string `json:"createOnlyProperties"`
}

// DescribeType fetches and decodes typeName's CloudFormation resource
// provider schema.
//
// Not yet consumed by this package's Get logic — each registration in
// register.go declares its LookupStrategy directly, verified against AWS's
// documentation rather than derived from this schema at runtime. DescribeType
// exists as client capability for the write-path workstream, which does need
// schema-driven property validation and createOnlyProperties-based replace
// detection.
func (c *Client) DescribeType(ctx context.Context, typeName string) (Schema, error) {
	out, err := c.cf.DescribeType(ctx, &cloudformation.DescribeTypeInput{
		Type:     cftypes.RegistryTypeResource,
		TypeName: aws.String(typeName),
	})
	if err != nil {
		var notFound *cftypes.TypeNotFoundException
		if errors.As(err, &notFound) {
			return Schema{}, kerrors.Wrap(err, kerrors.CodeValidation, "no CloudFormation resource provider schema is registered for %q", typeName)
		}
		return Schema{}, kerrors.Wrap(err, kerrors.CodeUnexpected, "describing type %s", typeName)
	}
	if out.Schema == nil {
		return Schema{}, kerrors.Validation("DescribeType for %s returned no schema", typeName)
	}

	var schema Schema
	if err := json.Unmarshal([]byte(*out.Schema), &schema); err != nil {
		return Schema{}, kerrors.Wrap(err, kerrors.CodeUnexpected, "decoding schema for %s", typeName)
	}
	return schema, nil
}
