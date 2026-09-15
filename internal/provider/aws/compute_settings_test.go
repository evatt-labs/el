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
		Runtime:      "python3.13",
		Architecture: "arm64",
		LayerArn:     "arn:aws:lambda:us-east-1:123456789012:layer:adapter:1",
		MemorySize:   1024,
		Timeout:      45,
		Env:          map[string]string{"LOG_LEVEL": "info"},
		EnvSecrets:   map[string]string{"DATABASE_URL": "DB.connection_uri"},
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
