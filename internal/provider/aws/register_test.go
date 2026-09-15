package aws

import (
	"context"
	"reflect"
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
		{Provider + "/" + TypeLambdaURL, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByAttr},
		{Provider + "/" + TypeEventsRule, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByName},
		{Provider + "/" + TypePermissionEventsRule, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByAttr},
		{Provider + "/" + TypePermissionAPIGateway, manifest.CapabilityCompute, resource.PhaseCompute, resource.LookupByAttr},
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
	bucket, role := byType[TypeArtifactBucket], byType[TypeIAMRole]
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

	// The artifact bucket and execution role apply to every compute
	// service unconditionally — every Lambda needs a package and a role
	// regardless of how it's invoked.
	for name, reg := range map[string]resource.Registration{"bucket": bucket, "role": role} {
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

	rulePermission, apiPermission := byType[TypePermissionEventsRule], byType[TypePermissionAPIGateway]
	if !rulePermission.AppliesToTrigger(manifest.TriggerSchedule) {
		t.Error("TypePermissionEventsRule must apply to a schedule-triggered service")
	}
	if rulePermission.AppliesToTrigger(manifest.TriggerHTTP) {
		t.Error("TypePermissionEventsRule must not apply to an HTTP-triggered service")
	}
	if !apiPermission.AppliesToTrigger(manifest.TriggerHTTP) {
		t.Error("TypePermissionAPIGateway must apply to an HTTP-triggered service")
	}
	if apiPermission.AppliesToTrigger(manifest.TriggerSchedule) {
		t.Error("TypePermissionAPIGateway must not apply to a schedule-triggered service")
	}
}

// TestHTTPFrontDoorRegistrationsAreMutuallyExclusive is PR #80's review
// fix at this package's own boundary: AWS::Lambda::Url and
// AWS::ApiGatewayV2::Api both apply to TriggerHTTP, so Triggers alone
// cannot stop a service planning both. SelectedBy must select exactly one,
// and its own accompanying permission (TypePermissionAPIGateway) must
// track ApiGatewayV2::Api's choice exactly — creating that permission for
// a service that has no API Gateway would name a SourceArn Cloud Control
// could never resolve.
func TestHTTPFrontDoorRegistrationsAreMutuallyExclusive(t *testing.T) {
	regs := Registrations(&Client{})
	byType := map[string]resource.Registration{}
	for _, r := range regs {
		byType[r.Type] = r
	}
	url, httpAPI, apiPermission := byType[TypeLambdaURL], byType[TypeAPIGatewayV2API], byType[TypePermissionAPIGateway]

	cases := []struct {
		name         string
		settings     map[string]any
		wantURL      bool
		wantAPI      bool
		wantAPIPerms bool
	}{
		{"unset settings default to apigateway", nil, false, true, true},
		{"empty settings default to apigateway", map[string]any{}, false, true, true},
		{"explicit apigateway", map[string]any{"httpFrontDoor": "apigateway"}, false, true, true},
		{"explicit url", map[string]any{"httpFrontDoor": "url"}, true, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := url.AppliesToSettings(c.settings); got != c.wantURL {
				t.Errorf("TypeLambdaURL.AppliesToSettings(%v) = %v, want %v", c.settings, got, c.wantURL)
			}
			if got := httpAPI.AppliesToSettings(c.settings); got != c.wantAPI {
				t.Errorf("TypeAPIGatewayV2API.AppliesToSettings(%v) = %v, want %v", c.settings, got, c.wantAPI)
			}
			if got := apiPermission.AppliesToSettings(c.settings); got != c.wantAPIPerms {
				t.Errorf("TypePermissionAPIGateway.AppliesToSettings(%v) = %v, want %v", c.settings, got, c.wantAPIPerms)
			}
			if url.AppliesToSettings(c.settings) && httpAPI.AppliesToSettings(c.settings) {
				t.Fatalf("both TypeLambdaURL and TypeAPIGatewayV2API select for settings=%v — a service would get two HTTP front doors", c.settings)
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
		TypeLambdaFunction, TypeLambdaURL, TypeEventsRule, TypePermissionEventsRule,
		TypeAPIGatewayV2API, TypePermissionAPIGateway, // PhaseCompute, registration order
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

// TestPermissionRegistrationsDeclareAListScope pins the fix for a real
// `kraai plan` failure against a live account: both TypePermissionEventsRule
// and TypePermissionAPIGateway drive AWS::Lambda::Permission, whose Cloud
// Control list handler is parent-scoped and rejects an unscoped
// ListResources call outright (see Client.ListResources's own doc comment
// for the real API error). Both registrations must wire a listScope that
// resolves to the invoking function's own derived name, not leave
// resourceType's engine to send the unscoped request that originally
// failed.
func TestPermissionRegistrationsDeclareAListScope(t *testing.T) {
	regs := Registrations(&Client{})
	for _, typeName := range []string{TypePermissionEventsRule, TypePermissionAPIGateway} {
		t.Run(typeName, func(t *testing.T) {
			var reg *resource.Registration
			for i := range regs {
				if regs[i].Type == typeName {
					reg = &regs[i]
				}
			}
			if reg == nil {
				t.Fatalf("no registration found for %s", typeName)
			}
			perm, ok := reg.Resource.(*lambdaPermissionResource)
			if !ok {
				t.Fatalf("Resource = %T, want *lambdaPermissionResource", reg.Resource)
			}
			if perm.inner.listScope == nil {
				t.Fatal("listScope is nil — this registration would send an unscoped ListResources request, exactly the failure this fix closes")
			}
			model, err := perm.inner.listScope("myenv-api")
			if err != nil {
				t.Fatalf("listScope: %v", err)
			}
			want := map[string]any{"FunctionName": "myenv-api"}
			if !reflect.DeepEqual(model, want) {
				t.Fatalf("listScope(%q) = %+v, want %+v", "myenv-api", model, want)
			}
		})
	}
}
