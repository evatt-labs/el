package aws

import (
	"context"
	"errors"
	"testing"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/resource"
)

// fakeClient is ccAPI, hand-rolled — no AWS account, no network (D21).
type fakeClient struct {
	// byIdentifier answers GetResource. A missing key means "not found",
	// distinct from an entry mapping to an error.
	byIdentifier map[string]map[string]any
	getErr       map[string]error
	list         []string
	listErr      error
	getCalls     []string
	listCalls    int
}

func (f *fakeClient) GetResource(_ context.Context, _ string, identifier string) (map[string]any, bool, error) {
	f.getCalls = append(f.getCalls, identifier)
	if err, ok := f.getErr[identifier]; ok {
		return nil, false, err
	}
	props, ok := f.byIdentifier[identifier]
	if !ok {
		return nil, false, nil
	}
	return props, true, nil
}

func (f *fakeClient) ListResources(context.Context, string) ([]string, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func matchNameField(properties map[string]any, name string) bool {
	v, _ := properties["Name"].(string)
	return v == name
}

func TestResourceTypeGetByName(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		fc := &fakeClient{byIdentifier: map[string]map[string]any{
			"my-bucket": {"BucketName": "my-bucket"},
		}}
		r := &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: fc}

		state, err := r.Get(context.Background(), resource.Ref{Name: "my-bucket"})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if state == nil || state.ID != "my-bucket" || state.Attributes["BucketName"] != "my-bucket" {
			t.Fatalf("state = %+v", state)
		}
		// No ListResources call for a byName type: the derived name already
		// is the primary identifier.
		if fc.listCalls != 0 {
			t.Fatalf("listCalls = %d, want 0", fc.listCalls)
		}
	})

	t.Run("absent is (nil, nil), never an error", func(t *testing.T) {
		fc := &fakeClient{byIdentifier: map[string]map[string]any{}}
		r := &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: fc}

		state, err := r.Get(context.Background(), resource.Ref{Name: "missing"})
		if err != nil || state != nil {
			t.Fatalf("state=%v err=%v, want (nil, nil)", state, err)
		}
	})

	t.Run("an API failure is never mistaken for absence", func(t *testing.T) {
		// This is the non-negotiable bar: teardown reads Get's (nil, nil) as
		// "already deleted, keep going". Misreading an outage this way would
		// orphan a real resource.
		fc := &fakeClient{getErr: map[string]error{"x": errors.New("throttled")}}
		r := &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: fc}

		state, err := r.Get(context.Background(), resource.Ref{Name: "x"})
		if err == nil {
			t.Fatal("expected the failure to be reported")
		}
		if state != nil {
			t.Fatalf("state = %v, want nil alongside the error", state)
		}
	})
}

func TestResourceTypeGetByAttr(t *testing.T) {
	t.Run("finds the matching candidate and reuses its properties", func(t *testing.T) {
		fc := &fakeClient{
			list: []string{"cand-1", "cand-2"},
			byIdentifier: map[string]map[string]any{
				"cand-1": {"Name": "other"},
				"cand-2": {"Name": "target"},
			},
		}
		r := &resourceType{provider: Provider, typeName: TypeCloudFrontDistribution, lookup: resource.LookupByAttr, client: fc, match: matchNameField}

		state, err := r.Get(context.Background(), resource.Ref{Name: "target"})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if state == nil || state.ID != "cand-2" {
			t.Fatalf("state = %+v", state)
		}
		// Exactly one GetResource per candidate up to and including the
		// match — never a second call for the identifier Get already has
		// properties for.
		if len(fc.getCalls) != 2 {
			t.Fatalf("getCalls = %v, want 2 (one per candidate, no re-fetch)", fc.getCalls)
		}
	})

	t.Run("no candidate matches is absence, not an error", func(t *testing.T) {
		fc := &fakeClient{
			list:         []string{"cand-1"},
			byIdentifier: map[string]map[string]any{"cand-1": {"Name": "other"}},
		}
		r := &resourceType{provider: Provider, typeName: TypeCloudFrontDistribution, lookup: resource.LookupByAttr, client: fc, match: matchNameField}

		state, err := r.Get(context.Background(), resource.Ref{Name: "target"})
		if err != nil || state != nil {
			t.Fatalf("state=%v err=%v, want (nil, nil)", state, err)
		}
	})

	t.Run("a candidate deleted between list and get is skipped, not fatal", func(t *testing.T) {
		fc := &fakeClient{
			list:         []string{"vanished", "cand-2"},
			byIdentifier: map[string]map[string]any{"cand-2": {"Name": "target"}},
		}
		r := &resourceType{provider: Provider, typeName: TypeCloudFrontDistribution, lookup: resource.LookupByAttr, client: fc, match: matchNameField}

		state, err := r.Get(context.Background(), resource.Ref{Name: "target"})
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if state == nil || state.ID != "cand-2" {
			t.Fatalf("state = %+v, want the surviving candidate", state)
		}
	})

	t.Run("ListResources failing is reported, not treated as no candidates", func(t *testing.T) {
		fc := &fakeClient{listErr: errors.New("throttled")}
		r := &resourceType{provider: Provider, typeName: TypeCloudFrontDistribution, lookup: resource.LookupByAttr, client: fc, match: matchNameField}

		if _, err := r.Get(context.Background(), resource.Ref{Name: "target"}); err == nil {
			t.Fatal("expected the ListResources failure to be reported")
		}
	})

	t.Run("GetResource failing on a candidate is reported, not skipped", func(t *testing.T) {
		fc := &fakeClient{
			list:   []string{"cand-1"},
			getErr: map[string]error{"cand-1": errors.New("throttled")},
		}
		r := &resourceType{provider: Provider, typeName: TypeCloudFrontDistribution, lookup: resource.LookupByAttr, client: fc, match: matchNameField}

		if _, err := r.Get(context.Background(), resource.Ref{Name: "target"}); err == nil {
			t.Fatal("expected the GetResource failure to be reported")
		}
	})
}

func TestResourceTypeMutationsAreNotImplemented(t *testing.T) {
	r := &resourceType{provider: Provider, typeName: TypeS3Bucket, lookup: resource.LookupByName, client: &fakeClient{}}

	assertNotImplemented := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected ErrNotImplemented")
		}
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("err = %v, want it to wrap ErrNotImplemented", err)
		}
		var kerr *kerrors.KError
		if !errors.As(err, &kerr) {
			t.Fatalf("err = %v, want a *kerrors.KError", err)
		}
	}

	t.Run("Create", func(t *testing.T) {
		_, err := r.Create(context.Background(), resource.Spec{})
		assertNotImplemented(t, err)
	})
	t.Run("Update", func(t *testing.T) {
		_, err := r.Update(context.Background(), resource.Ref{}, resource.Spec{})
		assertNotImplemented(t, err)
	})
	t.Run("Delete", func(t *testing.T) {
		assertNotImplemented(t, r.Delete(context.Background(), resource.Ref{}))
	})
}
