package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// maxListPages bounds a ListResources page walk, as a backstop against a
// NextToken that never stops advancing — the same defensive pattern the
// Neon client uses for its cursor walk.
const maxListPages = 100

// Default polling bounds for asynchronous Cloud Control operations
// (CreateResource, UpdateResource, DeleteResource). These are generous on
// the ceiling and cheap on the floor: a CloudFront distribution's
// propagation alone can run past fifteen minutes, while an S3 bucket create
// typically finishes in under a second, so the backoff starts small and
// grows rather than picking one fixed interval that is wrong for most
// resource types. WithPollTimings overrides all three, which is how tests
// exercise polling without real waiting.
const (
	defaultPollInitialDelay = 2 * time.Second
	defaultPollMaxDelay     = 30 * time.Second
	defaultPollTimeout      = 40 * time.Minute
)

// cloudControlAPI is the subset of *cloudcontrol.Client this package's
// *Client calls: the five verbs Cloud Control exposes uniformly (D17's
// finding) plus the status poll the async ones require.
//
// Shaped like the SDK's own method signatures rather than this package's
// vocabulary, unlike ccAPI in resource.go: this is the seam being
// substituted in client_test.go, and the thing on the other side of it is
// the SDK client itself, which is a concrete struct with no interface of
// its own to depend on instead.
type cloudControlAPI interface {
	GetResource(ctx context.Context, params *cloudcontrol.GetResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error)
	ListResources(ctx context.Context, params *cloudcontrol.ListResourcesInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error)
	CreateResource(ctx context.Context, params *cloudcontrol.CreateResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error)
	UpdateResource(ctx context.Context, params *cloudcontrol.UpdateResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error)
	DeleteResource(ctx context.Context, params *cloudcontrol.DeleteResourceInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error)
	GetResourceRequestStatus(ctx context.Context, params *cloudcontrol.GetResourceRequestStatusInput, optFns ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error)
}

// cloudFormationAPI is the subset of *cloudformation.Client this package
// calls: DescribeType, to fetch a resource type's schema.
type cloudFormationAPI interface {
	DescribeType(ctx context.Context, params *cloudformation.DescribeTypeInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeTypeOutput, error)
}

// s3API is the subset of *s3.Client this package calls: PutObject, to
// upload a Lambda deployment artifact to the per-environment artifact
// bucket (aws-provider-compute).
//
// Deliberately not Cloud-Control-routed like every other verb in this
// package: Cloud Control manages a bucket's own existence
// (AWS::S3::Bucket's Get/Create/Delete), but has no notion of "put this
// object in it" — object data planes are not part of the Cloud Control
// resource-provider surface for any type. This is the one place this
// package reaches past Cloud Control to a service-specific SDK client,
// for exactly the operation Cloud Control cannot express.
type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// stsAPI is the subset of *sts.Client this package calls: GetCallerIdentity,
// to resolve the AWS account id an ARN needs.
type stsAPI interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// Client is a thin Cloud Control + CloudFormation client.
//
// "Thin" here means its exported methods already speak this package's own
// vocabulary — a decoded properties map, a plain identifier list — rather
// than the SDK's Input/Output types. resource.go's ccAPI interface is
// satisfied by *Client precisely because of that shape, so nothing above
// this file needs to know the SDK exists.
type Client struct {
	cc  cloudControlAPI
	cf  cloudFormationAPI
	s3  s3API
	sts stsAPI

	// region is the resolved AWS region every ARN this package constructs
	// (see AccountID's doc comment) is built against. Set from the SDK
	// config's own resolved value in New, not from Settings.Region
	// directly — Settings.Region may be empty and deferred to the SDK's own
	// resolution chain (see Settings.Region's doc comment), and by the time
	// LoadDefaultConfig returns, cfg.Region already holds whatever that
	// chain actually settled on.
	region string

	// Polling bounds for the async verbs. Defaulted in New, overridable via
	// WithPollTimings.
	pollInitialDelay time.Duration
	pollMaxDelay     time.Duration
	pollTimeout      time.Duration

	// accountMu/accountID/accountLoaded cache the caller's AWS account id
	// for the process's lifetime, the same lazy-on-first-use, cache-only-
	// on-success shape resourceType.getSchema already uses and for the
	// identical reason: more than one Tier 2 resource (the Lambda's own
	// Role ARN, an EventBridge Rule's Target ARN) needs it, an account's id
	// cannot change mid-process, and a single transient STS throttle should
	// not be cached as a permanent failure.
	accountMu     sync.Mutex
	accountID     string
	accountLoaded bool
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

// WithS3API substitutes the S3 caller, which is how tests exercise artifact
// upload without an AWS account or network (D21).
func WithS3API(api s3API) Option {
	return func(c *Client) { c.s3 = api }
}

// WithSTSAPI substitutes the STS caller.
func WithSTSAPI(api stsAPI) Option {
	return func(c *Client) { c.sts = api }
}

// WithPollTimings overrides the backoff and overall timeout used to poll an
// asynchronous operation's ProgressEvent to a terminal state. Tests use this
// to exercise polling, timeout and cancellation behavior in milliseconds
// rather than the production defaults, which are sized for real Cloud
// Control propagation times (minutes, not milliseconds).
func WithPollTimings(initialDelay, maxDelay, timeout time.Duration) Option {
	return func(c *Client) {
		c.pollInitialDelay = initialDelay
		c.pollMaxDelay = maxDelay
		c.pollTimeout = timeout
	}
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
		cc:               cloudcontrol.NewFromConfig(cfg),
		cf:               cloudformation.NewFromConfig(cfg),
		s3:               s3.NewFromConfig(cfg),
		sts:              sts.NewFromConfig(cfg),
		region:           cfg.Region,
		pollInitialDelay: defaultPollInitialDelay,
		pollMaxDelay:     defaultPollMaxDelay,
		pollTimeout:      defaultPollTimeout,
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

// pollTimings resolves the effective backoff and timeout, falling back to
// the production defaults when a Client was built by a struct literal
// rather than New — every existing test in this package does exactly that
// (e.g. &Client{cc: cc}), and a zero-value delay would otherwise make the
// poll loop busy-spin instead of backing off.
func (c *Client) pollTimings() (initialDelay, maxDelay, timeout time.Duration) {
	initialDelay, maxDelay, timeout = c.pollInitialDelay, c.pollMaxDelay, c.pollTimeout
	if initialDelay <= 0 {
		initialDelay = defaultPollInitialDelay
	}
	if maxDelay <= 0 {
		maxDelay = defaultPollMaxDelay
	}
	if timeout <= 0 {
		timeout = defaultPollTimeout
	}
	return initialDelay, maxDelay, timeout
}

// pollToTerminal polls requestToken's status until it reaches a terminal
// OperationStatus (SUCCESS, FAILED or CANCEL_COMPLETE), or until an
// infra-level failure: the status call itself erroring, an empty response,
// context cancellation, or this poll's own timeout elapsing.
//
// It deliberately does not decide whether a terminal FAILED counts as an
// application-level error — Delete's "deleting something already absent is
// success" contract needs to inspect the terminal event's ErrorCode itself
// (NotFound is success for Delete, a real failure for Create/Update), so
// returning the raw terminal event and letting each verb interpret it keeps
// that decision where the contract actually lives, rather than baking one
// verb's success criteria into the shared poll loop.
//
// Backoff starts at initialDelay and doubles up to maxDelay on every
// non-terminal response, honouring Cloud Control's own RetryAfter hint when
// it is later than the computed backoff would be. Never spins without a
// delay: every iteration either returns or waits.
func (c *Client) pollToTerminal(ctx context.Context, requestToken, typeName, identifier string) (*cctypes.ProgressEvent, error) {
	initialDelay, maxDelay, timeout := c.pollTimings()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	delay := initialDelay
	var lastStatus cctypes.OperationStatus
	for {
		out, err := c.cc.GetResourceRequestStatus(ctx, &cloudcontrol.GetResourceRequestStatusInput{
			RequestToken: aws.String(requestToken),
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, kerrors.Wrap(ctx.Err(), kerrors.CodeUnexpected,
					"polling %s %q timed out or was cancelled (last known status %q)", typeName, identifier, lastStatus)
			}
			return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "polling status of %s %q", typeName, identifier)
		}
		if out.ProgressEvent == nil {
			return nil, kerrors.Validation("polling %s %q returned no progress event", typeName, identifier)
		}
		event := out.ProgressEvent
		lastStatus = event.OperationStatus

		switch event.OperationStatus {
		case cctypes.OperationStatusSuccess, cctypes.OperationStatusFailed, cctypes.OperationStatusCancelComplete:
			return event, nil
		}

		wait := delay
		if event.RetryAfter != nil {
			if until := time.Until(*event.RetryAfter); until > wait {
				wait = until
			}
		}
		select {
		case <-ctx.Done():
			return nil, kerrors.Wrap(ctx.Err(), kerrors.CodeUnexpected,
				"polling %s %q timed out or was cancelled (last known status %q)", typeName, identifier, lastStatus)
		case <-time.After(wait):
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// terminalFailureCodes are the HandlerErrorCodes that mean the caller did
// something the API will never accept as-is — a validation problem, not an
// unexpected platform failure. Everything else (throttling, internal
// errors, network failures, an unset code) is CodeUnexpected: retryable or
// unknown, not something the caller can fix by changing the manifest.
var terminalFailureCodes = map[cctypes.HandlerErrorCode]bool{
	cctypes.HandlerErrorCodeNotUpdatable:                 true,
	cctypes.HandlerErrorCodeInvalidRequest:               true,
	cctypes.HandlerErrorCodeAccessDenied:                 true,
	cctypes.HandlerErrorCodeUnauthorizedTaggingOperation: true,
	cctypes.HandlerErrorCodeInvalidCredentials:           true,
	cctypes.HandlerErrorCodeAlreadyExists:                true,
	cctypes.HandlerErrorCodeNotFound:                     true,
	cctypes.HandlerErrorCodeResourceConflict:             true,
	cctypes.HandlerErrorCodeServiceLimitExceeded:         true,
}

// translateFailure maps a terminal FAILED/CANCEL_COMPLETE ProgressEvent onto
// a kerrors bucket, naming the resource and the last status per this
// workstream's requirement that a stuck or failed operation return a useful
// error rather than a bare "it failed."
func translateFailure(op, typeName, identifier string, event *cctypes.ProgressEvent) error {
	statusMessage := "no status message"
	if event.StatusMessage != nil && *event.StatusMessage != "" {
		statusMessage = *event.StatusMessage
	}

	code := kerrors.CodeUnexpected
	if terminalFailureCodes[event.ErrorCode] {
		code = kerrors.CodeValidation
	}
	return kerrors.Wrap(errors.New(statusMessage), code,
		"%s %s %q: operation ended %s (error code %q)", op, typeName, identifier, event.OperationStatus, event.ErrorCode)
}

// decodeResourceModel decodes a terminal ProgressEvent's ResourceModel —
// "a JSON string containing the resource model, consisting of each resource
// property and its current value" per Cloud Control's own documentation —
// into this package's plain properties map. Read from the ProgressEvent
// itself rather than issuing a follow-up GetResource: Cloud Control already
// promises the final model on SUCCESS, and a follow-up call would just be
// an extra round trip to re-fetch what the operation already returned.
func decodeResourceModel(model *string) (map[string]any, error) {
	if model == nil || *model == "" {
		return map[string]any{}, nil
	}
	var properties map[string]any
	if err := json.Unmarshal([]byte(*model), &properties); err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "decoding resource model")
	}
	return properties, nil
}

// CreateResource submits desiredState for creation and polls to a terminal
// state, returning the provider-assigned identifier and the resulting
// properties.
func (c *Client) CreateResource(ctx context.Context, typeName string, desiredState map[string]any) (string, map[string]any, error) {
	body, err := json.Marshal(desiredState)
	if err != nil {
		return "", nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "encoding desired state for %s", typeName)
	}

	out, err := c.cc.CreateResource(ctx, &cloudcontrol.CreateResourceInput{
		TypeName:     aws.String(typeName),
		DesiredState: aws.String(string(body)),
	})
	if err != nil {
		return "", nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "creating %s", typeName)
	}
	if out.ProgressEvent == nil || out.ProgressEvent.RequestToken == nil {
		return "", nil, kerrors.Validation("CreateResource for %s returned no request token", typeName)
	}

	event, err := c.pollToTerminal(ctx, *out.ProgressEvent.RequestToken, typeName, "<pending>")
	if err != nil {
		return "", nil, err
	}
	if event.OperationStatus != cctypes.OperationStatusSuccess {
		identifier := ""
		if event.Identifier != nil {
			identifier = *event.Identifier
		}
		return "", nil, translateFailure("creating", typeName, identifier, event)
	}

	identifier := ""
	if event.Identifier != nil {
		identifier = *event.Identifier
	}
	properties, err := decodeResourceModel(event.ResourceModel)
	if err != nil {
		return "", nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "decoding created %s %q", typeName, identifier)
	}
	return identifier, properties, nil
}

// UpdateResource submits patch (an RFC 6902 JSON Patch document) against
// identifier and polls to a terminal state, returning the resulting
// properties.
func (c *Client) UpdateResource(ctx context.Context, typeName, identifier string, patch []byte) (map[string]any, error) {
	out, err := c.cc.UpdateResource(ctx, &cloudcontrol.UpdateResourceInput{
		TypeName:      aws.String(typeName),
		Identifier:    aws.String(identifier),
		PatchDocument: aws.String(string(patch)),
	})
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "updating %s %q", typeName, identifier)
	}
	if out.ProgressEvent == nil || out.ProgressEvent.RequestToken == nil {
		return nil, kerrors.Validation("UpdateResource for %s %q returned no request token", typeName, identifier)
	}

	event, err := c.pollToTerminal(ctx, *out.ProgressEvent.RequestToken, typeName, identifier)
	if err != nil {
		return nil, err
	}
	if event.OperationStatus != cctypes.OperationStatusSuccess {
		return nil, translateFailure("updating", typeName, identifier, event)
	}

	properties, err := decodeResourceModel(event.ResourceModel)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "decoding updated %s %q", typeName, identifier)
	}
	return properties, nil
}

// DeleteResource submits a delete for identifier and polls to a terminal
// state. Deleting something already absent is success, exactly as Get
// reports absence rather than failure (see resource.Resource's doc
// comment): that holds both when Cloud Control rejects the delete
// synchronously with ResourceNotFoundException, and when the async delete
// handler itself discovers the resource is already gone and reports a
// terminal FAILED with HandlerErrorCodeNotFound.
func (c *Client) DeleteResource(ctx context.Context, typeName, identifier string) error {
	out, err := c.cc.DeleteResource(ctx, &cloudcontrol.DeleteResourceInput{
		TypeName:   aws.String(typeName),
		Identifier: aws.String(identifier),
	})
	if err != nil {
		var notFound *cctypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil
		}
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "deleting %s %q", typeName, identifier)
	}
	if out.ProgressEvent == nil || out.ProgressEvent.RequestToken == nil {
		return kerrors.Validation("DeleteResource for %s %q returned no request token", typeName, identifier)
	}

	event, err := c.pollToTerminal(ctx, *out.ProgressEvent.RequestToken, typeName, identifier)
	if err != nil {
		return err
	}
	if event.OperationStatus == cctypes.OperationStatusSuccess {
		return nil
	}
	if event.ErrorCode == cctypes.HandlerErrorCodeNotFound {
		return nil
	}
	return translateFailure("deleting", typeName, identifier, event)
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
	// rather than an in-place update — the authoritative source for
	// plan's replace-or-update decision, consumed via resourceType's
	// DiffersFromState (plan.ImmutableDiffer).
	CreateOnlyProperties []string `json:"createOnlyProperties"`
	// Handlers lists this type's implemented verbs by name ("create",
	// "read", "update", "delete", "list"). Decoded as raw JSON because this
	// package only ever asks whether a key is present — an
	// IMMUTABLE-provisioning type (create/read/delete, no update handler)
	// omits "update" entirely rather than declaring it empty, so presence of
	// the key is the whole signal HasUpdateHandler needs.
	Handlers map[string]json.RawMessage `json:"handlers"`
}

// HasUpdateHandler reports whether this type's schema declares an update
// handler at all. A type without one cannot be reconciled in place — Update
// must refuse with resource.ErrImmutable rather than attempt a call Cloud
// Control will reject, per this workstream's brief.
func (s Schema) HasUpdateHandler() bool {
	_, ok := s.Handlers["update"]
	return ok
}

// DescribeType fetches and decodes typeName's CloudFormation resource
// provider schema.
//
// Fetched at runtime on first use per type and cached for the process's
// lifetime (resourceType.getSchema), never vendored: D17's own finding
// measured schema sizes up to 116KB, and a resource type's schema does not
// change within a single kraai invocation, so one DescribeType call per type
// per process is the right amount of caching — enough to avoid repeating an
// expensive call on every Get/Create/Update/DiffersFromState, not so much
// that a real schema change (a new AWS API version) would need vendored
// files kept in sync by hand.
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

// Region returns the AWS region this Client resolved at construction (see
// the region field's own doc comment).
func (c *Client) Region() string { return c.region }

// PutObject uploads body to bucket/key, replacing any existing object at
// that key.
//
// No existence check first: S3 PutObject is itself an overwrite, so a
// HeadObject-then-PutObject round trip would only add a request without
// changing the outcome. This package's callers additionally never call it
// with the same key twice for different content — the artifact key is a
// content hash (aws-provider-compute's zip determinism), so a repeat
// PutObject for an unchanged input writes back bytes S3 already holds.
func (c *Client) PutObject(ctx context.Context, bucket, key string, body []byte) error {
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	})
	if err != nil {
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "uploading s3://%s/%s", bucket, key)
	}
	return nil
}

// AccountID returns the AWS account id the configured credentials
// authenticate as, fetched once via STS GetCallerIdentity and cached for
// the process's lifetime (see the Client.accountID field doc for why
// caching, and why only on success).
//
// # Why this exists: ARN construction without a live cross-resource lookup
//
// A Lambda's execution Role property and an EventBridge Rule's Target Arn
// both require a full ARN, not a bare name — unlike AWS::Lambda::Url's
// TargetFunctionArn, which documents bare-name acceptance. The role and the
// function it assumes into are ordered by phase (PhaseStorage before
// PhaseCompute, see register.go), so a live GetResource lookup of the
// role's Arn attribute would be safe. The function and its own triggers
// (EventBridge Rule, Lambda Url) are not: both are registered in
// PhaseCompute, which runs concurrently and gives no guarantee the function
// exists yet when its rule's Create runs (the same class of same-phase
// ordering gap register.go's own doc comment already flags for
// RecordSet/CloudFront/Certificate). Constructing every ARN this package
// needs locally, from the account id plus the region plus the resource's
// own derived name, removes the dependency entirely rather than papering
// over a race with a retry.
//
// Assumes the "aws" partition. kraai's stated first deployment target is
// commercial AWS for kraai.dev's own infrastructure (D24); GovCloud/China
// partitions, whose ARNs use "aws-us-gov"/"aws-cn", are not something this
// workstream verified against and are out of scope here.
func (c *Client) AccountID(ctx context.Context) (string, error) {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	if c.accountLoaded {
		return c.accountID, nil
	}

	out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", kerrors.Wrap(err, kerrors.CodeUnexpected, "resolving the AWS account id via STS")
	}
	if out.Account == nil || *out.Account == "" {
		return "", kerrors.Validation("STS GetCallerIdentity returned no account id")
	}

	c.accountID = *out.Account
	c.accountLoaded = true
	return c.accountID, nil
}
