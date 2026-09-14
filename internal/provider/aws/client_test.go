package aws

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cctypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// fakeCC is a hand-rolled cloudControlAPI: no AWS account, no network (D21).
type fakeCC struct {
	getOut  *cloudcontrol.GetResourceOutput
	getErr  error
	listOut []*cloudcontrol.ListResourcesOutput
	listErr error
	listAt  int
	gotReq  []*cloudcontrol.GetResourceInput
	listReq []*cloudcontrol.ListResourcesInput
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
