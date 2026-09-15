package aws

import (
	"reflect"
	"testing"
)

func TestDecodeLambdaSettings(t *testing.T) {
	settings := map[string]any{
		"runtime":      "python3.13",
		"architecture": "arm64",
		"layerArn":     "arn:aws:lambda:us-east-1:123456789012:layer:adapter:1",
		"memorySize":   1024,
		"timeout":      float64(45), // encoding/json round trip shape
		"env":          map[string]any{"LOG_LEVEL": "info"},
		"envSecrets":   map[string]any{"DATABASE_URL": "DB.connection_uri"},
	}

	got, err := decodeLambdaSettings(settings)
	if err != nil {
		t.Fatalf("decodeLambdaSettings: %v", err)
	}
	want := LambdaSettings{
		Runtime:       "python3.13",
		Architecture:  "arm64",
		LayerArn:      "arn:aws:lambda:us-east-1:123456789012:layer:adapter:1",
		MemorySize:    1024,
		Timeout:       45,
		Env:           map[string]string{"LOG_LEVEL": "info"},
		EnvSecrets:    map[string]string{"DATABASE_URL": "DB.connection_uri"},
		HTTPFrontDoor: httpFrontDoorAPIGateway,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeLambdaSettings = %+v, want %+v", got, want)
	}
}

func TestDecodeLambdaSettingsDefaults(t *testing.T) {
	settings := map[string]any{
		"runtime":      "python3.13",
		"architecture": "x86_64",
		"layerArn":     "arn:aws:lambda:us-east-1:123456789012:layer:adapter:1",
	}
	got, err := decodeLambdaSettings(settings)
	if err != nil {
		t.Fatalf("decodeLambdaSettings: %v", err)
	}
	if got.MemorySize != defaultMemorySize {
		t.Errorf("MemorySize = %d, want default %d", got.MemorySize, defaultMemorySize)
	}
	if got.Timeout != defaultTimeout {
		t.Errorf("Timeout = %d, want default %d", got.Timeout, defaultTimeout)
	}
	if got.Env != nil || got.EnvSecrets != nil {
		t.Errorf("Env/EnvSecrets = %+v/%+v, want both nil when absent", got.Env, got.EnvSecrets)
	}
}

func TestDecodeLambdaSettingsMissingRequired(t *testing.T) {
	cases := []map[string]any{
		{"architecture": "arm64", "layerArn": "arn:x"},
		{"runtime": "python3.13", "layerArn": "arn:x"},
		{"runtime": "python3.13", "architecture": "arm64"},
		{},
	}
	for _, settings := range cases {
		if _, err := decodeLambdaSettings(settings); err == nil {
			t.Errorf("decodeLambdaSettings(%+v): expected a validation error", settings)
		}
	}
}

func TestDecodeLambdaSettingsHTTPFrontDoor(t *testing.T) {
	base := map[string]any{"runtime": "python3.13", "architecture": "arm64", "layerArn": "arn:x"}

	t.Run("unset defaults to apigateway", func(t *testing.T) {
		got, err := decodeLambdaSettings(base)
		if err != nil {
			t.Fatalf("decodeLambdaSettings: %v", err)
		}
		if got.HTTPFrontDoor != httpFrontDoorAPIGateway {
			t.Fatalf("HTTPFrontDoor = %q, want %q", got.HTTPFrontDoor, httpFrontDoorAPIGateway)
		}
	})

	t.Run("explicit url is honored", func(t *testing.T) {
		settings := map[string]any{}
		for k, v := range base {
			settings[k] = v
		}
		settings["httpFrontDoor"] = "url"
		got, err := decodeLambdaSettings(settings)
		if err != nil {
			t.Fatalf("decodeLambdaSettings: %v", err)
		}
		if got.HTTPFrontDoor != httpFrontDoorURL {
			t.Fatalf("HTTPFrontDoor = %q, want %q", got.HTTPFrontDoor, httpFrontDoorURL)
		}
	})

	t.Run("an unrecognized value is a validation error, not a silent no-op", func(t *testing.T) {
		settings := map[string]any{}
		for k, v := range base {
			settings[k] = v
		}
		settings["httpFrontDoor"] = "lambda-url-please"
		if _, err := decodeLambdaSettings(settings); err == nil {
			t.Fatal("expected a validation error for an unrecognized httpFrontDoor value")
		}
	})
}

func TestHTTPFrontDoorIs(t *testing.T) {
	isURL := httpFrontDoorIs(httpFrontDoorURL)
	isAPIGateway := httpFrontDoorIs(httpFrontDoorAPIGateway)

	cases := []struct {
		name     string
		settings map[string]any
	}{
		{"nil settings", nil},
		{"empty settings", map[string]any{}},
		{"explicit apigateway", map[string]any{"httpFrontDoor": "apigateway"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if isURL(c.settings) {
				t.Errorf("httpFrontDoorIs(url)(%v) = true, want false", c.settings)
			}
			if !isAPIGateway(c.settings) {
				t.Errorf("httpFrontDoorIs(apigateway)(%v) = false, want true (the default)", c.settings)
			}
		})
	}

	urlSettings := map[string]any{"httpFrontDoor": "url"}
	if !isURL(urlSettings) {
		t.Error("httpFrontDoorIs(url) did not match an explicit url setting")
	}
	if isAPIGateway(urlSettings) {
		t.Error("httpFrontDoorIs(apigateway) matched an explicit url setting")
	}
}

func TestDecodeLambdaSettingsManagedPolicyArns(t *testing.T) {
	settings := map[string]any{
		"runtime": "python3.13", "architecture": "arm64", "layerArn": "arn:x",
		"managedPolicyArns": []any{"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess", 42, ""},
	}
	got, err := decodeLambdaSettings(settings)
	if err != nil {
		t.Fatalf("decodeLambdaSettings: %v", err)
	}
	want := []string{"arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess"}
	if !reflect.DeepEqual(got.ManagedPolicyArns, want) {
		t.Fatalf("ManagedPolicyArns = %+v, want %+v (non-string/empty entries dropped)", got.ManagedPolicyArns, want)
	}
}
