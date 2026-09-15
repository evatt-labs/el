package aws

import "testing"

func TestDecodeSettings(t *testing.T) {
	t.Run("reads region", func(t *testing.T) {
		s, err := DecodeSettings(map[string]any{"region": "us-east-1"})
		if err != nil {
			t.Fatalf("DecodeSettings: %v", err)
		}
		if s.Region != "us-east-1" {
			t.Fatalf("Region = %q", s.Region)
		}
	})

	t.Run("missing region defers to the SDK's own default chain", func(t *testing.T) {
		s, err := DecodeSettings(map[string]any{})
		if err != nil {
			t.Fatalf("DecodeSettings: %v", err)
		}
		if s.Region != "" {
			t.Fatalf("Region = %q, want empty so New defers to the SDK's default chain", s.Region)
		}
	})

	t.Run("region present but the wrong type is a real error, not treated as missing", func(t *testing.T) {
		_, err := DecodeSettings(map[string]any{"region": 42})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}
