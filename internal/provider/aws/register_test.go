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
		{Provider + "/" + TypeCertificateManagerCertificate, manifest.CapabilityObjects, resource.PhaseStorage, resource.LookupByTag},
		{Provider + "/" + TypeRoute53HostedZone, manifest.CapabilityObjects, resource.PhaseStorage, resource.LookupByAPI},
		{Provider + "/" + TypeRoute53RecordSet, manifest.CapabilityObjects, resource.PhaseCompute, resource.LookupByAttr},
		{Provider + "/" + TypeLambdaFunction, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByName},
		{Provider + "/" + TypeAPIGatewayV2API, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByTag},
		{Provider + "/" + TypeArtifactBucket, manifest.CapabilityCompute, resource.PhaseStorage, resource.LookupByName},
		{Provider + "/" + TypeIAMRole, manifest.CapabilityCompute, resource.PhaseStorage, resource.LookupByName},
		{Provider + "/" + TypeSSMParameter, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByName},
		{Provider + "/" + TypeLambdaURL, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByAttr},
		{Provider + "/" + TypeEventsRule, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByName},
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

// TestComputeRegistrationsTriggerGating is the per-service-compute fix
// pinned at this package's own boundary: the API Gateway registration only
// applies to an HTTP-triggered service, while the Lambda function
// registration applies regardless — the exact shape that stops a
// schedule-invoked service like kraai-api's `tick` from planning an API
// Gateway nothing will ever call.
func TestComputeRegistrationsTriggerGating(t *testing.T) {
	regs := Registrations(&Client{})

	byType := map[string]resource.Registration{}
	for _, r := range regs {
		byType[r.Type] = r
	}
	function, httpAPI := byType[TypeLambdaFunction], byType[TypeAPIGatewayV2API]
	bucket, role, param := byType[TypeArtifactBucket], byType[TypeIAMRole], byType[TypeSSMParameter]
	url, rule := byType[TypeLambdaURL], byType[TypeEventsRule]

	if function.Triggers != nil {
		t.Fatalf("TypeLambdaFunction.Triggers = %v, want nil: every service gets a function regardless of trigger", function.Triggers)
	}
	if !function.AppliesToTrigger(manifest.TriggerHTTP) || !function.AppliesToTrigger(manifest.TriggerSchedule) || !function.AppliesToTrigger("") {
		t.Fatal("TypeLambdaFunction must apply to every trigger, including none declared")
	}

	if !httpAPI.AppliesToTrigger(manifest.TriggerHTTP) {
		t.Error("TypeAPIGatewayV2API must apply to an HTTP-triggered service")
	}
	if httpAPI.AppliesToTrigger(manifest.TriggerSchedule) {
		t.Error("TypeAPIGatewayV2API must not apply to a schedule-triggered service — this is the bug this workstream fixes")
	}
	if !httpAPI.AppliesToTrigger("") {
		t.Error("TypeAPIGatewayV2API must still apply to a service declaring no compute: block, unchanged from before Triggers existed")
	}

	// The artifact bucket, execution role and artifact-key parameter apply
	// to every compute service unconditionally — every Lambda needs a
	// package, a role and (this package's own choice) a discoverable
	// artifact-key parameter regardless of how it's invoked.
	for name, reg := range map[string]resource.Registration{"bucket": bucket, "role": role, "param": param} {
		if reg.Triggers != nil {
			t.Errorf("%s.Triggers = %v, want nil", name, reg.Triggers)
		}
		if !reg.AppliesToTrigger(manifest.TriggerHTTP) || !reg.AppliesToTrigger(manifest.TriggerSchedule) || !reg.AppliesToTrigger("") {
			t.Errorf("%s must apply to every trigger", name)
		}
	}

	if !url.AppliesToTrigger(manifest.TriggerHTTP) {
		t.Error("TypeLambdaURL must apply to an HTTP-triggered service")
	}
	if url.AppliesToTrigger(manifest.TriggerSchedule) {
		t.Error("TypeLambdaURL must not apply to a schedule-triggered service")
	}

	if !rule.AppliesToTrigger(manifest.TriggerSchedule) {
		t.Error("TypeEventsRule must apply to a schedule-triggered service")
	}
	if rule.AppliesToTrigger(manifest.TriggerHTTP) {
		t.Error("TypeEventsRule must not apply to an HTTP-triggered service")
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

	objects, err := reg.Resolve(manifest.CapabilityObjects, map[string]string{manifest.CapabilityObjects: Provider})
	if err != nil {
		t.Fatalf("Resolve objects: %v", err)
	}
	wantObjects := []string{TypeRoute53HostedZone, TypeCertificateManagerCertificate, TypeS3Bucket, TypeCloudFrontDistribution, TypeRoute53RecordSet}
	if len(objects) != len(wantObjects) {
		t.Fatalf("objects = %+v, want %d entries", objects, len(wantObjects))
	}
	// Resolve sorts by Phase ascending (stable within a phase), so this
	// order also proves HostedZone, Certificate and S3Bucket all land in
	// PhaseStorage, ahead of CloudFrontDistribution and RecordSet in
	// PhaseCompute — CloudFront can reference the certificate PhaseStorage
	// already provisioned by the time it runs.
	for i, want := range wantObjects {
		if objects[i].Type != want {
			t.Fatalf("objects[%d].Type = %q, want %q (full: %+v)", i, objects[i].Type, want, objects)
		}
	}
	if objects[0].Phase != resource.PhaseStorage || objects[2].Phase != resource.PhaseStorage {
		t.Fatalf("objects = %+v, want the first three in PhaseStorage", objects)
	}
	if objects[3].Phase != resource.PhaseCompute || objects[4].Phase != resource.PhaseCompute {
		t.Fatalf("objects = %+v, want the last two in PhaseCompute", objects)
	}

	compute, err := reg.Resolve(manifest.CapabilityCompute, map[string]string{manifest.CapabilityCompute: Provider})
	if err != nil {
		t.Fatalf("Resolve compute: %v", err)
	}
	wantCompute := []string{
		TypeArtifactBucket, TypeIAMRole, // PhaseStorage, registration order
		TypeLambdaFunction, TypeSSMParameter, TypeLambdaURL, TypeEventsRule, TypeAPIGatewayV2API, // PhaseCompute, registration order
	}
	if len(compute) != len(wantCompute) {
		t.Fatalf("compute = %+v, want %d entries", compute, len(wantCompute))
	}
	for i, want := range wantCompute {
		if compute[i].Type != want {
			t.Fatalf("compute[%d].Type = %q, want %q (full: %+v)", i, compute[i].Type, want, compute)
		}
	}
	if compute[0].Phase != resource.PhaseStorage || compute[1].Phase != resource.PhaseStorage {
		t.Fatalf("compute = %+v, want the artifact bucket and role in PhaseStorage, ahead of the function that needs both", compute)
	}
	for _, r := range compute[2:] {
		if r.Phase != resource.PhaseCompute {
			t.Fatalf("compute = %+v, want everything after the bucket/role in PhaseCompute", compute)
		}
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
