package aws

import (
	"context"
	"strings"
	"testing"

	"github.com/evatt-labs/kraai/internal/resource"
)

func TestArtifactBucketName(t *testing.T) {
	got := artifactBucketName("myenv-myservice")
	want := "myenv-myservice-artifacts"
	if got != want {
		t.Fatalf("artifactBucketName(%q) = %q, want %q", "myenv-myservice", got, want)
	}

	// A long derived name must still respect S3's 63-character ceiling
	// after the suffix is appended, and must not end in a hyphen.
	long := strings.Repeat("a", 60)
	longGot := artifactBucketName(long)
	if len(longGot) > maxBucketNameLen {
		t.Fatalf("artifactBucketName(%d-char name) = %d chars, want <= %d", len(long), len(longGot), maxBucketNameLen)
	}
	if strings.HasSuffix(longGot, "-") {
		t.Fatalf("artifactBucketName(%d-char name) = %q, ends in a hyphen", len(long), longGot)
	}
}

func TestArtifactBucketResourceRewritesNameBothWays(t *testing.T) {
	const serviceName = "myenv-api"
	const realBucket = "myenv-api-artifacts"

	fc := &fakeClient{
		byIdentifier: map[string]map[string]any{realBucket: {"BucketName": realBucket}},
		createID:     realBucket,
		createProps:  map[string]any{"BucketName": realBucket},
	}
	bucket := &artifactBucketResource{
		inner: &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: fc},
	}

	t.Run("Get resolves the real bucket name but reports the service Ref", func(t *testing.T) {
		state, err := bucket.Get(context.Background(), resource.Ref{Provider: Provider, Type: TypeArtifactBucket, Name: serviceName})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if state == nil {
			t.Fatal("Get: expected a state")
		}
		if state.Ref.Name != serviceName {
			t.Fatalf("state.Ref.Name = %q, want the caller's own Ref.Name %q, not the real bucket name", state.Ref.Name, serviceName)
		}
		if len(fc.getCalls) == 0 || fc.getCalls[0] != realBucket {
			t.Fatalf("GetResource called with %v, want the transformed bucket name %q", fc.getCalls, realBucket)
		}
	})

	t.Run("Create submits the real bucket name and reports the service Ref", func(t *testing.T) {
		state, err := bucket.Create(context.Background(), resource.Spec{Name: serviceName})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if state.Ref.Name != serviceName {
			t.Fatalf("state.Ref.Name = %q, want %q", state.Ref.Name, serviceName)
		}
		if fc.createCalls[0]["BucketName"] != nil {
			t.Fatalf("unexpected BucketName in desired state: %+v", fc.createCalls[0])
		}
	})

	t.Run("Delete resolves the real bucket name", func(t *testing.T) {
		if err := bucket.Delete(context.Background(), resource.Ref{Name: serviceName}); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if len(fc.deleteCalls) == 0 || fc.deleteCalls[len(fc.deleteCalls)-1] != realBucket {
			t.Fatalf("DeleteResource called with %v, want %q", fc.deleteCalls, realBucket)
		}
	})

	t.Run("Update is refused", func(t *testing.T) {
		if _, err := bucket.Update(context.Background(), resource.Ref{Name: serviceName}, resource.Spec{}); err == nil {
			t.Fatal("expected Update to be refused")
		}
	})
}
