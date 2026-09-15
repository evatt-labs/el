package aws

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/evatt-labs/kraai/internal/resource"
)

func newSSMParameterResourceForTest(fc *fakeClient) *ssmParameterResource {
	return &ssmParameterResource{
		inner: &resourceType{provider: Provider, typeName: TypeSSMParameter, lookup: resource.LookupByName, client: fc},
	}
}

func TestArtifactParameterPath(t *testing.T) {
	got := artifactParameterPath("myenv-api")
	want := "/kraai/myenv-api/artifact-key"
	if got != want {
		t.Fatalf("artifactParameterPath(%q) = %q, want %q", "myenv-api", got, want)
	}
}

func TestSSMParameterCreatePublishesTheArtifactKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, sha256Hex, err := buildArtifact(dir)
	if err != nil {
		t.Fatalf("buildArtifact: %v", err)
	}

	fc := &fakeClient{createID: "p", createProps: map[string]any{}}
	param := newSSMParameterResourceForTest(fc)

	spec := resource.Spec{Name: "myenv-api", Config: map[string]any{"dir": dir}}
	state, err := param.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state.Ref.Name != "myenv-api" {
		t.Fatalf("state.Ref.Name = %q, want the service name, not the parameter path", state.Ref.Name)
	}

	desired := fc.createCalls[0]
	if desired["Name"] != "/kraai/myenv-api/artifact-key" {
		t.Fatalf("Name = %v", desired["Name"])
	}
	if desired["Type"] != "String" {
		t.Fatalf("Type = %v, want String", desired["Type"])
	}
	wantValue := artifactObjectKey("myenv-api", sha256Hex)
	if desired["Value"] != wantValue {
		t.Fatalf("Value = %v, want %q (the same deterministic key lambda.go would upload to)", desired["Value"], wantValue)
	}
}

func TestSSMParameterUpdateAndDiffersFromState(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("x\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fc := &fakeClient{
		byIdentifier: map[string]map[string]any{"/kraai/myenv-api/artifact-key": {"Value": "old"}},
		updateProps:  map[string]any{"Value": "new"},
		schema:       Schema{Handlers: map[string]json.RawMessage{"update": json.RawMessage(`{}`)}},
	}
	param := newSSMParameterResourceForTest(fc)

	spec := resource.Spec{Name: "myenv-api", Config: map[string]any{"dir": dir}}
	if _, err := param.Update(context.Background(), resource.Ref{Name: "myenv-api"}, spec); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := param.DiffersFromState(spec, &resource.State{Attributes: map[string]any{"Name": "/kraai/myenv-api/artifact-key"}}); err != nil {
		t.Fatalf("DiffersFromState: %v", err)
	}
}

func TestSSMParameterGetAndDeleteUseTheRealPath(t *testing.T) {
	fc := &fakeClient{
		byIdentifier: map[string]map[string]any{"/kraai/myenv-api/artifact-key": {"Value": "myenv-api/abc.zip"}},
	}
	param := newSSMParameterResourceForTest(fc)

	state, err := param.Get(context.Background(), resource.Ref{Name: "myenv-api"})
	if err != nil || state == nil {
		t.Fatalf("Get: state=%v err=%v", state, err)
	}
	if state.Ref.Name != "myenv-api" {
		t.Fatalf("state.Ref.Name = %q, want %q", state.Ref.Name, "myenv-api")
	}
	if err := param.Delete(context.Background(), resource.Ref{Name: "myenv-api"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fc.deleteCalls[0] != "/kraai/myenv-api/artifact-key" {
		t.Fatalf("Delete called with %q, want the real parameter path", fc.deleteCalls[0])
	}
}
