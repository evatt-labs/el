package aws

import "testing"

func TestCloudfrontMatch(t *testing.T) {
	cases := []struct {
		name       string
		properties map[string]any
		want       string
		match      bool
	}{
		{
			name: "matches an alias in the list",
			properties: map[string]any{
				"DistributionConfig": map[string]any{
					"Aliases": []any{"other.example.com", "target.example.com"},
				},
			},
			want:  "target.example.com",
			match: true,
		},
		{
			name: "no matching alias",
			properties: map[string]any{
				"DistributionConfig": map[string]any{
					"Aliases": []any{"other.example.com"},
				},
			},
			want:  "target.example.com",
			match: false,
		},
		{
			name: "a distribution with no aliases configured at all cannot be found this way",
			properties: map[string]any{
				"DistributionConfig": map[string]any{},
			},
			want:  "target.example.com",
			match: false,
		},
		{
			name:       "DistributionConfig missing entirely",
			properties: map[string]any{},
			want:       "target.example.com",
			match:      false,
		},
		{
			name: "Aliases present but not a list",
			properties: map[string]any{
				"DistributionConfig": map[string]any{"Aliases": "not-a-list"},
			},
			want:  "target.example.com",
			match: false,
		},
		{
			name: "a non-string element in Aliases is skipped, not fatal",
			properties: map[string]any{
				"DistributionConfig": map[string]any{"Aliases": []any{42, "target.example.com"}},
			},
			want:  "target.example.com",
			match: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cloudfrontMatch(tc.properties, tc.want); got != tc.match {
				t.Fatalf("cloudfrontMatch = %v, want %v", got, tc.match)
			}
		})
	}
}

func TestApigatewayv2Match(t *testing.T) {
	cases := []struct {
		name       string
		properties map[string]any
		want       string
		match      bool
	}{
		{
			name:       "matches the identity tag",
			properties: map[string]any{"Tags": map[string]any{identityTagKey: "my-api"}},
			want:       "my-api",
			match:      true,
		},
		{
			name:       "a different tag value does not match",
			properties: map[string]any{"Tags": map[string]any{identityTagKey: "other-api"}},
			want:       "my-api",
			match:      false,
		},
		{
			name:       "an untagged API cannot be found this way",
			properties: map[string]any{"Tags": map[string]any{}},
			want:       "my-api",
			match:      false,
		},
		{
			name:       "Tags missing entirely",
			properties: map[string]any{},
			want:       "my-api",
			match:      false,
		},
		{
			name:       "Tags present but not a map",
			properties: map[string]any{"Tags": "not-a-map"},
			want:       "my-api",
			match:      false,
		},
		{
			name:       "the identity tag present but not a string",
			properties: map[string]any{"Tags": map[string]any{identityTagKey: 42}},
			want:       "my-api",
			match:      false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := apigatewayv2Match(tc.properties, tc.want); got != tc.match {
				t.Fatalf("apigatewayv2Match = %v, want %v", got, tc.match)
			}
		})
	}
}
