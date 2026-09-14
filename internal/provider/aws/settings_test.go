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

	t.Run("missing region is a single named error", func(t *testing.T) {
		_, err := DecodeSettings(map[string]any{})
		if err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("region present but the wrong type is treated as missing", func(t *testing.T) {
		_, err := DecodeSettings(map[string]any{"region": 42})
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}
