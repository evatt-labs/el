package aws

import "github.com/evatt-labs/kraai/internal/kerrors"

// Settings are the AWS-specific values a manifest's providers.compute (or
// providers.objects) settings block carries for this provider (D34).
//
// Free-form in the manifest — internal/manifest's Provider.Settings is
// uninterpreted map[string]any — and decoded here, in one place, so a
// missing field is reported once with every other missing field rather than
// failing on the first one a caller happens to touch.
type Settings struct {
	// Region is the AWS region every Cloud Control and CloudFormation call
	// in this package targets.
	Region string
}

// DecodeSettings reads Settings out of a manifest provider's Settings map.
// Exported so a caller assembling the registry can validate settings before
// wiring anything up, rather than at first use.
func DecodeSettings(config map[string]any) (Settings, error) {
	s := Settings{Region: str(config, "region")}

	var missing []string
	if s.Region == "" {
		missing = append(missing, "region")
	}
	if len(missing) > 0 {
		return Settings{}, kerrors.Validation("aws provider is missing required settings: %v", missing)
	}
	return s, nil
}

func str(config map[string]any, key string) string {
	v, _ := config[key].(string)
	return v
}
