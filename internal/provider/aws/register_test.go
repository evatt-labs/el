package aws

import (
	"context"
	"testing"

	"github.com/evatt-labs/kraai/internal/manifest"
	"github.com/evatt-labs/kraai/internal/resource"
)

func TestRegisterWiresEveryType(t *testing.T) {
	reg := resource.NewRegistry()
	client := &Client{}
	if err := Register(reg, client); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cases := []struct {
		key        string
		capability string
		phase      resource.Phase
		lookup     resource.LookupStrategy
	}{
		{Provider + "/" + TypeS3Bucket, manifest.CapabilityObjects, resource.PhaseStorage, resource.LookupByName},
		{Provider + "/" + TypeCloudFrontDistribution, manifest.CapabilityObjects, resource.PhaseCompute, resource.LookupByAttr},
		{Provider + "/" + TypeLambdaFunction, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByName},
		{Provider + "/" + TypeAPIGatewayV2API, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByTag},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := reg.Lookup(tc.key)
			if !ok {
				t.Fatalf("Lookup(%q): not registered", tc.key)
			}
			if got.Capability != tc.capability || got.Phase != tc.phase || got.Lookup != tc.lookup {
				t.Fatalf("registration = %+v, want capability=%s phase=%v lookup=%s", got, tc.capability, tc.phase, tc.lookup)
			}
		})
	}
}

func TestRegisterPropagatesADuplicateRegistrationError(t *testing.T) {
	// Registry.Register already rejects a duplicate provider/type key
	// (tested in internal/resource); this proves Register's own loop
	// surfaces that failure to its caller instead of swallowing it after
	// registering some but not all four types.
	reg := resource.NewRegistry()
	if err := reg.Register(Registrations(&Client{})[0]); err != nil {
		t.Fatalf("seeding a conflicting registration: %v", err)
	}

	if err := Register(reg, &Client{}); err == nil {
		t.Fatal("expected the duplicate S3 bucket registration to be reported")
	}
}

func TestRegisterExpandsCapabilitiesInPhaseOrder(t *testing.T) {
	// Mirrors neonresource's D30 pairing: one capability, two AWS types, the
	// producer resolved before its consumer.
	reg := resource.NewRegistry()
	if err := Register(reg, &Client{}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	objects, err := reg.Resolve(manifest.CapabilityObjects, Provider)
	if err != nil {
		t.Fatalf("Resolve objects: %v", err)
	}
	if len(objects) != 2 || objects[0].Type != TypeS3Bucket || objects[1].Type != TypeCloudFrontDistribution {
		t.Fatalf("objects = %+v, want [S3Bucket, CloudFrontDistribution] in that order", objects)
	}

	compute, err := reg.Resolve(manifest.CapabilityCompute, Provider)
	if err != nil {
		t.Fatalf("Resolve compute: %v", err)
	}
	if len(compute) != 2 || compute[0].Type != TypeLambdaFunction || compute[1].Type != TypeAPIGatewayV2API {
		t.Fatalf("compute = %+v, want [LambdaFunction, ApiGatewayV2Api]", compute)
	}
}

// TestRegistrationsGetThroughTheRegistry is a light end-to-end check that the
// registry's Resource is actually this package's resourceType wired to the
// client passed to Register — not just metadata.
func TestRegistrationsGetThroughTheRegistry(t *testing.T) {
	fc := &fakeClient{byIdentifier: map[string]map[string]any{
		"my-function": {"FunctionName": "my-function"},
	}}
	regs := Registrations(nil)
	for i := range regs {
		if regs[i].Type == TypeLambdaFunction {
			regs[i].Resource = &resourceType{provider: Provider, typeName: TypeLambdaFunction, lookup: resource.LookupByName, client: fc}
		}
	}
	reg := resource.NewRegistry()
	for _, r := range regs {
		if err := reg.Register(r); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	got, ok := reg.Lookup(Provider + "/" + TypeLambdaFunction)
	if !ok {
		t.Fatal("lambda function not registered")
	}
	state, err := got.Resource.Get(context.Background(), resource.Ref{Name: "my-function"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if state == nil || state.ID != "my-function" {
		t.Fatalf("state = %+v", state)
	}
}
