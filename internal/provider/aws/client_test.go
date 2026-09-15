package aws

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// testPollTimings makes the poll loop resolve near-instantly: a
// microsecond initial/max delay and a timeout generous enough for a
// handful of iterations, but never so long that a genuinely stuck test
// makes the suite hang.
func testPollTimings() Option {
	return WithPollTimings(time.Microsecond, time.Microsecond, 200*time.Millisecond)
}

// fakeCC is a hand-rolled cloudControlAPI: no AWS account, no network (D21).
type fakeCC struct {
	getOut  *cloudcontrol.GetResourceOutput
	getErr  error
	listOut []*cloudcontrol.ListResourcesOutput
	listErr error
	listAt  int
	gotReq  []*cloudcontrol.GetResourceInput
	listReq []*cloudcontrol.ListResourcesInput

	createOut *cloudcontrol.CreateResourceOutput
	createErr error
	createReq []*cloudcontrol.CreateResourceInput

	updateOut *cloudcontrol.UpdateResourceOutput
	updateErr error
	updateReq []*cloudcontrol.UpdateResourceInput

	deleteOut *cloudcontrol.DeleteResourceOutput
	deleteErr error
	deleteReq []*cloudcontrol.DeleteResourceInput

	// statusOut is a sequence of responses returned in order, one per
	// GetResourceRequestStatus call, so a test can script
	// PENDING -> IN_PROGRESS -> SUCCESS/FAILED. The last entry repeats once
	// exhausted, the same pattern listOut/listAt already use.
	statusOut   []*cloudcontrol.GetResourceRequestStatusOutput
	statusErr   error
	statusAt    int
	statusCalls int
}

func (f *fakeCC) GetResource(_ context.Context, params *cloudcontrol.GetResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceOutput, error) {
	f.gotReq = append(f.gotReq, params)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getOut, nil
}

func (f *fakeCC) ListResources(_ context.Context, params *cloudcontrol.ListResourcesInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.ListResourcesOutput, error) {
	f.listReq = append(f.listReq, params)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := f.listOut[f.listAt]
	if f.listAt < len(f.listOut)-1 {
		f.listAt++
	}
	return out, nil
}

func (f *fakeCC) CreateResource(_ context.Context, params *cloudcontrol.CreateResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.CreateResourceOutput, error) {
	f.createReq = append(f.createReq, params)
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.createOut, nil
}

func (f *fakeCC) UpdateResource(_ context.Context, params *cloudcontrol.UpdateResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.UpdateResourceOutput, error) {
	f.updateReq = append(f.updateReq, params)
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.updateOut, nil
}

func (f *fakeCC) DeleteResource(_ context.Context, params *cloudcontrol.DeleteResourceInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.DeleteResourceOutput, error) {
	f.deleteReq = append(f.deleteReq, params)
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return f.deleteOut, nil
}

func (f *fakeCC) GetResourceRequestStatus(ctx context.Context, _ *cloudcontrol.GetResourceRequestStatusInput, _ ...func(*cloudcontrol.Options)) (*cloudcontrol.GetResourceRequestStatusOutput, error) {
	f.statusCalls++
	if f.statusErr != nil {
		return nil, f.statusErr
	}
	if len(f.statusOut) == 0 {
		return &cloudcontrol.GetResourceRequestStatusOutput{}, nil
	}
	out := f.statusOut[f.statusAt]
	if f.statusAt < len(f.statusOut)-1 {
		f.statusAt++
	}
	// Give a stuck-forever test (no terminal status ever scripted) a chance
	// to observe context cancellation instead of spinning until the
	// suite's own test timeout.
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return out, nil
}

type fakeCF struct {
	out *cloudformation.DescribeTypeOutput
	err error
}

func (f *fakeCF) DescribeType(context.Context, *cloudformation.DescribeTypeInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeTypeOutput, error) {
	return f.out, f.err
}

func TestClientGetResource(t *testing.T) {
	t.Run("decodes properties", func(t *testing.T) {
		cc := &fakeCC{getOut: &cloudcontrol.GetResourceOutput{
			ResourceDescription: &cctypes.ResourceDescription{
				Identifier: aws.String("my-bucket"),
				Properties: aws.String(`{"BucketName":"my-bucket","Arn":"arn:aws:s3:::my-bucket"}`),
			},
		}}
		c := &Client{cc: cc}

		props, found, err := c.GetResource(context.Background(), TypeS3Bucket, "my-bucket")
		if err != nil || !found {
			t.Fatalf("GetResource: found=%v err=%v", found, err)
		}
		if props["BucketName"] != "my-bucket" {
			t.Fatalf("properties = %+v", props)
		}
		if got := *cc.gotReq[0].TypeName; got != TypeS3Bucket {
			t.Fatalf("TypeName = %q", got)
		}
	})

	t.Run("translates ResourceNotFoundException to absence, not error", func(t *testing.T) {
		cc := &fakeCC{getErr: &cctypes.ResourceNotFoundException{Message: aws.String("gone")}}
		c := &Client{cc: cc}

		props, found, err := c.GetResource(context.Background(), TypeS3Bucket, "missing")
		if err != nil {
			t.Fatalf("expected no error for a not-found resource, got %v", err)
		}
		if found || props != nil {
			t.Fatalf("found=%v props=%v, want absence", found, props)
		}
	})

	t.Run("a non-not-found error is never read as absence", func(t *testing.T) {
		// This is the bar the read-only Get contract exists to hold: an
		// outage must never look like "already deleted" to a caller, because
		// teardown treats absence as success.
		cc := &fakeCC{getErr: &cctypes.ThrottlingException{Message: aws.String("slow down")}}
		c := &Client{cc: cc}

		props, found, err := c.GetResource(context.Background(), TypeS3Bucket, "x")
		if err == nil {
			t.Fatal("expected a throttling error to be reported, not swallowed")
		}
		if found || props != nil {
			t.Fatalf("found=%v props=%v, want no result alongside the error", found, props)
		}
	})

	t.Run("a nil ResourceDescription is an error, not absence", func(t *testing.T) {
		cc := &fakeCC{getOut: &cloudcontrol.GetResourceOutput{}}
		c := &Client{cc: cc}

		_, found, err := c.GetResource(context.Background(), TypeS3Bucket, "x")
		if err == nil || found {
			t.Fatalf("found=%v err=%v, want an error", found, err)
		}
	})

	t.Run("malformed properties JSON is an error", func(t *testing.T) {
		cc := &fakeCC{getOut: &cloudcontrol.GetResourceOutput{
			ResourceDescription: &cctypes.ResourceDescription{
				Identifier: aws.String("x"),
				Properties: aws.String(`{not json`),
			},
		}}
		c := &Client{cc: cc}

		_, _, err := c.GetResource(context.Background(), TypeS3Bucket, "x")
		if err == nil {
			t.Fatal("expected a decode error")
		}
	})
}

func TestClientListResources(t *testing.T) {
	t.Run("collects identifiers across pages", func(t *testing.T) {
		cc := &fakeCC{listOut: []*cloudcontrol.ListResourcesOutput{
			{
				ResourceDescriptions: []cctypes.ResourceDescription{
					{Identifier: aws.String("a")}, {Identifier: aws.String("b")},
				},
				NextToken: aws.String("page-2"),
			},
			{
				ResourceDescriptions: []cctypes.ResourceDescription{{Identifier: aws.String("c")}},
			},
		}}
		c := &Client{cc: cc}

		ids, err := c.ListResources(context.Background(), TypeLambdaFunction)
		if err != nil {
			t.Fatalf("ListResources: %v", err)
		}
		want := []string{"a", "b", "c"}
		if len(ids) != len(want) {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
		for i, id := range want {
			if ids[i] != id {
				t.Fatalf("ids[%d] = %q, want %q", i, ids[i], id)
			}
		}
		if len(cc.listReq) != 2 || *cc.listReq[1].NextToken != "page-2" {
			t.Fatalf("second page request = %+v", cc.listReq)
		}
	})

	t.Run("TypeNotFoundException is reported, not treated as empty", func(t *testing.T) {
		cc := &fakeCC{listErr: &cctypes.TypeNotFoundException{Message: aws.String("no such type")}}
		c := &Client{cc: cc}

		ids, err := c.ListResources(context.Background(), "AWS::Bogus::Type")
		if err == nil {
			t.Fatal("expected an error")
		}
		if ids != nil {
			t.Fatalf("ids = %v, want nil alongside the error", ids)
		}
	})

	t.Run("another error is not treated as empty either", func(t *testing.T) {
		cc := &fakeCC{listErr: &cctypes.ThrottlingException{Message: aws.String("slow down")}}
		c := &Client{cc: cc}

		if _, err := c.ListResources(context.Background(), TypeLambdaFunction); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("a NextToken that never stops is bounded, not an infinite loop", func(t *testing.T) {
		cc := &fakeCC{listOut: []*cloudcontrol.ListResourcesOutput{{
			ResourceDescriptions: []cctypes.ResourceDescription{{Identifier: aws.String("a")}},
			NextToken:            aws.String("always-more"),
		}}}
		c := &Client{cc: cc}

		_, err := c.ListResources(context.Background(), TypeLambdaFunction)
		if err == nil {
			t.Fatal("expected the page walk to be bounded and report an error")
		}
		if len(cc.listReq) != maxListPages {
			t.Fatalf("made %d requests, want exactly %d", len(cc.listReq), maxListPages)
		}
	})
}

func TestClientDescribeType(t *testing.T) {
	t.Run("decodes the schema fields this package uses", func(t *testing.T) {
		cf := &fakeCF{out: &cloudformation.DescribeTypeOutput{
			Schema: aws.String(`{"primaryIdentifier":["/properties/Id"],"createOnlyProperties":["/properties/DistributionConfig"]}`),
		}}
		c := &Client{cf: cf}

		schema, err := c.DescribeType(context.Background(), TypeCloudFrontDistribution)
		if err != nil {
			t.Fatalf("DescribeType: %v", err)
		}
		if len(schema.PrimaryIdentifier) != 1 || schema.PrimaryIdentifier[0] != "/properties/Id" {
			t.Fatalf("PrimaryIdentifier = %v", schema.PrimaryIdentifier)
		}
		if len(schema.CreateOnlyProperties) != 1 {
			t.Fatalf("CreateOnlyProperties = %v", schema.CreateOnlyProperties)
		}
	})

	t.Run("TypeNotFoundException is a validation error", func(t *testing.T) {
		cf := &fakeCF{err: &cftypes.TypeNotFoundException{Message: aws.String("no such type")}}
		c := &Client{cf: cf}

		_, err := c.DescribeType(context.Background(), "AWS::Bogus::Type")
		if err == nil {
			t.Fatal("expected an error")
		}
		var kerr *kerrors.KError
		if !errors.As(err, &kerr) || kerr.Code() != kerrors.CodeValidation {
			t.Fatalf("err = %v, want a CodeValidation KError", err)
		}
	})

	t.Run("another error is wrapped, not swallowed", func(t *testing.T) {
		cf := &fakeCF{err: errors.New("boom")}
		c := &Client{cf: cf}

		if _, err := c.DescribeType(context.Background(), TypeS3Bucket); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("a nil schema is an error", func(t *testing.T) {
		cf := &fakeCF{out: &cloudformation.DescribeTypeOutput{}}
		c := &Client{cf: cf}

		if _, err := c.DescribeType(context.Background(), TypeS3Bucket); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed schema JSON is an error", func(t *testing.T) {
		cf := &fakeCF{out: &cloudformation.DescribeTypeOutput{Schema: aws.String(`{not json`)}}
		c := &Client{cf: cf}

		if _, err := c.DescribeType(context.Background(), TypeS3Bucket); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestNew(t *testing.T) {
	t.Run("applies options over the default SDK-backed clients", func(t *testing.T) {
		cc := &fakeCC{getOut: &cloudcontrol.GetResourceOutput{
			ResourceDescription: &cctypes.ResourceDescription{Properties: aws.String(`{"ok":true}`)},
		}}
		cf := &fakeCF{out: &cloudformation.DescribeTypeOutput{Schema: aws.String(`{}`)}}

		c, err := New(context.Background(), Settings{Region: "us-east-1"}, WithCloudControlAPI(cc), WithCloudFormationAPI(cf))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, _, err := c.GetResource(context.Background(), TypeS3Bucket, "x"); err != nil {
			t.Fatalf("GetResource through the injected fake: %v", err)
		}
		if _, err := c.DescribeType(context.Background(), TypeS3Bucket); err != nil {
			t.Fatalf("DescribeType through the injected fake: %v", err)
		}
	})

	t.Run("empty region defers to the SDK's own default chain instead of failing", func(t *testing.T) {
		// No manifest region, and nothing to force LoadDefaultConfig itself
		// to fail (unlike the malformed-shared-config subtest below): this
		// proves settings.Region == "" is a legitimate "let the SDK decide"
		// signal, not an error condition New has to reject.
		if _, err := New(context.Background(), Settings{}); err != nil {
			t.Fatalf("New with an empty region: %v", err)
		}
	})

	t.Run("a config load failure is reported, not swallowed", func(t *testing.T) {
		// A malformed shared config file is a deterministic way to make the
		// SDK's own LoadDefaultConfig fail without touching the network or
		// real credentials.
		dir := t.TempDir()
		badConfig := filepath.Join(dir, "config")
		if err := os.WriteFile(badConfig, []byte("[profile broken\nkey = value with no closing bracket above"), 0o600); err != nil {
			t.Fatalf("writing fixture: %v", err)
		}
		t.Setenv("AWS_CONFIG_FILE", badConfig)
		t.Setenv("AWS_SDK_LOAD_CONFIG", "1")
		t.Setenv("AWS_PROFILE", "broken")

		if _, err := New(context.Background(), Settings{Region: "us-east-1"}); err == nil {
			t.Fatal("expected a config-loading error to be reported")
		}
	})
}

func TestClientCreateResource(t *testing.T) {
	t.Run("submits desired state and returns the created identifier and properties", func(t *testing.T) {
		cc := &fakeCC{
			createOut: &cloudcontrol.CreateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusSuccess,
					Identifier:      aws.String("my-bucket"),
					ResourceModel:   aws.String(`{"BucketName":"my-bucket"}`),
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		id, props, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{"BucketName": "my-bucket"})
		if err != nil {
			t.Fatalf("CreateResource: %v", err)
		}
		if id != "my-bucket" || props["BucketName"] != "my-bucket" {
			t.Fatalf("id=%q props=%+v", id, props)
		}
		if len(cc.createReq) != 1 || *cc.createReq[0].TypeName != TypeS3Bucket {
			t.Fatalf("createReq = %+v", cc.createReq)
		}
	})

	t.Run("polls through PENDING and IN_PROGRESS before succeeding", func(t *testing.T) {
		cc := &fakeCC{
			createOut: &cloudcontrol.CreateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{
				{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusPending}},
				{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusInProgress}},
				{ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusSuccess,
					Identifier:      aws.String("id-1"),
				}},
			},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		id, _, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{})
		if err != nil {
			t.Fatalf("CreateResource: %v", err)
		}
		if id != "id-1" {
			t.Fatalf("id = %q", id)
		}
		if cc.statusCalls != 3 {
			t.Fatalf("statusCalls = %d, want 3", cc.statusCalls)
		}
	})

	t.Run("a validation-shaped failure maps to CodeValidation", func(t *testing.T) {
		cc := &fakeCC{
			createOut: &cloudcontrol.CreateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusFailed,
					ErrorCode:       cctypes.HandlerErrorCodeAlreadyExists,
					StatusMessage:   aws.String("a bucket with this name already exists"),
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		_, _, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{})
		if err == nil {
			t.Fatal("expected a failure")
		}
		var kerr *kerrors.KError
		if !errors.As(err, &kerr) || kerr.Code() != kerrors.CodeValidation {
			t.Fatalf("err = %v, want CodeValidation", err)
		}
	})

	t.Run("an unrecognized failure maps to CodeUnexpected", func(t *testing.T) {
		cc := &fakeCC{
			createOut: &cloudcontrol.CreateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusFailed,
					ErrorCode:       cctypes.HandlerErrorCodeInternalFailure,
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		_, _, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{})
		if err == nil {
			t.Fatal("expected a failure")
		}
		var kerr *kerrors.KError
		if !errors.As(err, &kerr) || kerr.Code() != kerrors.CodeUnexpected {
			t.Fatalf("err = %v, want CodeUnexpected", err)
		}
	})

	t.Run("a CreateResource call failure is reported, not swallowed", func(t *testing.T) {
		cc := &fakeCC{createErr: errors.New("throttled")}
		c := &Client{cc: cc}

		if _, _, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{}); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("no request token in the response is an error", func(t *testing.T) {
		cc := &fakeCC{createOut: &cloudcontrol.CreateResourceOutput{}}
		c := &Client{cc: cc}

		if _, _, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{}); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("polling never resolving is bounded by the timeout, not an infinite loop", func(t *testing.T) {
		cc := &fakeCC{
			createOut: &cloudcontrol.CreateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{
				{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusInProgress}},
			},
		}
		c := &Client{cc: cc}
		c.pollInitialDelay, c.pollMaxDelay, c.pollTimeout = time.Millisecond, time.Millisecond, 20*time.Millisecond

		start := time.Now()
		_, _, err := c.CreateResource(context.Background(), TypeS3Bucket, map[string]any{})
		if err == nil {
			t.Fatal("expected the poll timeout to produce an error")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("took %v, want it bounded near the 20ms timeout", elapsed)
		}
	})

	t.Run("context cancellation mid-poll stops the loop and is reported", func(t *testing.T) {
		cc := &fakeCC{
			createOut: &cloudcontrol.CreateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{
				{ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusInProgress}},
			},
		}
		c := &Client{cc: cc}
		c.pollInitialDelay, c.pollMaxDelay, c.pollTimeout = time.Millisecond, time.Millisecond, time.Minute

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		_, _, err := c.CreateResource(ctx, TypeS3Bucket, map[string]any{})
		if err == nil {
			t.Fatal("expected cancellation to produce an error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("took %v, want it to stop promptly after cancellation", elapsed)
		}
	})
}

func TestClientUpdateResource(t *testing.T) {
	t.Run("submits the patch and returns updated properties", func(t *testing.T) {
		cc := &fakeCC{
			updateOut: &cloudcontrol.UpdateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusSuccess,
					ResourceModel:   aws.String(`{"BucketName":"my-bucket","Tags":[]}`),
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		props, err := c.UpdateResource(context.Background(), TypeS3Bucket, "my-bucket", []byte(`[{"op":"replace","path":"/Tags","value":[]}]`))
		if err != nil {
			t.Fatalf("UpdateResource: %v", err)
		}
		if props["BucketName"] != "my-bucket" {
			t.Fatalf("props = %+v", props)
		}
		if len(cc.updateReq) != 1 || *cc.updateReq[0].Identifier != "my-bucket" {
			t.Fatalf("updateReq = %+v", cc.updateReq)
		}
	})

	t.Run("a failure is mapped through translateFailure", func(t *testing.T) {
		cc := &fakeCC{
			updateOut: &cloudcontrol.UpdateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusFailed,
					ErrorCode:       cctypes.HandlerErrorCodeNotUpdatable,
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		if _, err := c.UpdateResource(context.Background(), TypeS3Bucket, "x", []byte(`[]`)); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("an UpdateResource call failure is reported, not swallowed", func(t *testing.T) {
		cc := &fakeCC{updateErr: errors.New("throttled")}
		c := &Client{cc: cc}

		if _, err := c.UpdateResource(context.Background(), TypeS3Bucket, "x", []byte(`[]`)); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("no request token in the response is an error", func(t *testing.T) {
		cc := &fakeCC{updateOut: &cloudcontrol.UpdateResourceOutput{}}
		c := &Client{cc: cc}

		if _, err := c.UpdateResource(context.Background(), TypeS3Bucket, "x", []byte(`[]`)); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed ResourceModel JSON is an error", func(t *testing.T) {
		cc := &fakeCC{
			updateOut: &cloudcontrol.UpdateResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusSuccess,
					ResourceModel:   aws.String(`{not json`),
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		if _, err := c.UpdateResource(context.Background(), TypeS3Bucket, "x", []byte(`[]`)); err == nil {
			t.Fatal("expected a decode error")
		}
	})
}

func TestClientDeleteResource(t *testing.T) {
	t.Run("polls to success", func(t *testing.T) {
		cc := &fakeCC{
			deleteOut: &cloudcontrol.DeleteResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{OperationStatus: cctypes.OperationStatusSuccess},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		if err := c.DeleteResource(context.Background(), TypeS3Bucket, "my-bucket"); err != nil {
			t.Fatalf("DeleteResource: %v", err)
		}
	})

	t.Run("a synchronous ResourceNotFoundException is success, not an error", func(t *testing.T) {
		cc := &fakeCC{deleteErr: &cctypes.ResourceNotFoundException{Message: aws.String("gone")}}
		c := &Client{cc: cc}

		if err := c.DeleteResource(context.Background(), TypeS3Bucket, "missing"); err != nil {
			t.Fatalf("DeleteResource: %v, want nil for an already-absent resource", err)
		}
	})

	t.Run("an async terminal NotFound is success, not an error", func(t *testing.T) {
		// The delete handler itself discovered the resource was already
		// gone by the time it ran — same absence-is-success contract as the
		// synchronous exception case above, reached through the async path
		// instead.
		cc := &fakeCC{
			deleteOut: &cloudcontrol.DeleteResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusFailed,
					ErrorCode:       cctypes.HandlerErrorCodeNotFound,
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		if err := c.DeleteResource(context.Background(), TypeS3Bucket, "vanished"); err != nil {
			t.Fatalf("DeleteResource: %v, want nil for an already-absent resource", err)
		}
	})

	t.Run("a real failure is still reported", func(t *testing.T) {
		cc := &fakeCC{
			deleteOut: &cloudcontrol.DeleteResourceOutput{
				ProgressEvent: &cctypes.ProgressEvent{RequestToken: aws.String("token-1")},
			},
			statusOut: []*cloudcontrol.GetResourceRequestStatusOutput{{
				ProgressEvent: &cctypes.ProgressEvent{
					OperationStatus: cctypes.OperationStatusFailed,
					ErrorCode:       cctypes.HandlerErrorCodeAccessDenied,
				},
			}},
		}
		c := &Client{cc: cc}
		testPollTimings()(c)

		if err := c.DeleteResource(context.Background(), TypeS3Bucket, "x"); err == nil {
			t.Fatal("expected an access-denied failure to be reported")
		}
	})

	t.Run("a DeleteResource call failure other than not-found is reported", func(t *testing.T) {
		cc := &fakeCC{deleteErr: errors.New("throttled")}
		c := &Client{cc: cc}

		if err := c.DeleteResource(context.Background(), TypeS3Bucket, "x"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("no request token in the response is an error", func(t *testing.T) {
		cc := &fakeCC{deleteOut: &cloudcontrol.DeleteResourceOutput{}}
		c := &Client{cc: cc}

		if err := c.DeleteResource(context.Background(), TypeS3Bucket, "x"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestClientPollTimingsDefaults(t *testing.T) {
	// A Client built by struct literal (every existing test in this
	// package does exactly that) has zero-value poll fields; pollTimings
	// must fall back to the production defaults rather than let the poll
	// loop busy-spin with no delay at all.
	c := &Client{}
	initial, maxDelay, timeout := c.pollTimings()
	if initial != defaultPollInitialDelay || maxDelay != defaultPollMaxDelay || timeout != defaultPollTimeout {
		t.Fatalf("pollTimings() = (%v, %v, %v), want the defaults", initial, maxDelay, timeout)
	}
}
