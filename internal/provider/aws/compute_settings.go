package aws

import "github.com/evatt-labs/kraai/internal/kerrors"

// LambdaSettings are the AWS Lambda-specific values a compute Spec's merged
// settings block carries, decoded per call from Spec.Config["settings"]
// rather than once at registration.
//
// # Why per call, not once at registration (contrast with neonresource.BranchSettings)
//
// A Neon branch has no per-binding settings override in the manifest schema
// today, so neonresource decodes BranchSettings once from
// providers.database.settings and closes over it. Compute settings are
// different: internal/plan's expandCompute already layers a service's own
// Compute.Settings over providers.compute.settings per top-level key
// (manifest.MergeSettings) before it ever reaches this package, precisely
// so one service can override, say, memorySize without every other
// service's Lambda inheriting it. Decoding once at registration would
// throw that override away — every service would get the provider's
// settings only, silently. Decoding from Spec.Config on every call is what
// lets the merge planner already performs actually reach the function.
type LambdaSettings struct {
	// Runtime is the Lambda runtime identifier, e.g. "python3.13".
	Runtime string
	// Architecture is the instruction set Lambda runs the function on, e.g.
	// "arm64" or "x86_64". CloudFormation's Architectures property is an
	// array, but Lambda accepts exactly one entry per function — a second
	// entry is rejected by the API itself — so this is a single string here
	// and wrapped into a one-element array when building the desired state.
	Architecture string
	// LayerArn is the ARN of the Lambda Web Adapter layer this function's
	// deployment package runs under. kraai does not build or publish this
	// layer itself (aws-provider-compute's brief): a manifest author
	// supplies the ARN of a layer version they published or one of AWS's
	// own published adapter layer ARNs.
	LayerArn string
	// MemorySize and Timeout configure the function's resource allocation
	// and maximum execution duration in seconds. Optional: defaultMemorySize
	// and defaultTimeout apply when unset, rather than forcing every
	// manifest to state them.
	MemorySize int
	Timeout    int
	// Env is literal (non-secret) environment variable values, passed
	// through to the function's Environment.Variables unchanged.
	Env map[string]string
	// EnvSecrets maps an environment variable name to the credential
	// contract's namespaced secret key — e.g. {"DATABASE_URL":
	// "DB.connection_uri"} when the service declares a database binding
	// named "DB". The binding name is whatever string the manifest author
	// wrote here; this package never hardcodes one, satisfying the
	// credential contract's namespacing (own binding's secrets bare,
	// other readable bindings' secrets as "<binding>.<name>") without this
	// package needing to know the manifest's binding vocabulary at all —
	// it only ever passes the string through to Spec.Secret.
	EnvSecrets map[string]string
	// ManagedPolicyArns are additional IAM managed policy ARNs to attach to
	// the function's execution role, alongside the
	// AWSLambdaBasicExecutionRole policy every execution role gets
	// unconditionally (CloudWatch Logs access; a function that cannot
	// write its own logs is not usefully deployable).
	ManagedPolicyArns []string
}

const (
	defaultMemorySize = 512
	defaultTimeout    = 30
)

// decodeLambdaSettings reads LambdaSettings out of a compute Spec's merged
// settings map (Spec.Config["settings"]).
//
// Runtime, Architecture and LayerArn are required: without them there is no
// deployable function (no interpreter, no instruction set, no adapter to
// run an ASGI app under). Everything else defaults.
func decodeLambdaSettings(settings map[string]any) (LambdaSettings, error) {
	s := LambdaSettings{
		Runtime:      settingStr(settings, "runtime"),
		Architecture: settingStr(settings, "architecture"),
		LayerArn:     settingStr(settings, "layerArn"),
		MemorySize:   settingInt(settings, "memorySize", defaultMemorySize),
		Timeout:      settingInt(settings, "timeout", defaultTimeout),
		Env:          settingStrMap(settings, "env"),
		EnvSecrets:   settingStrMap(settings, "envSecrets"),
	}
	if arns, ok := settings["managedPolicyArns"].([]any); ok {
		for _, a := range arns {
			if str, ok := a.(string); ok && str != "" {
				s.ManagedPolicyArns = append(s.ManagedPolicyArns, str)
			}
		}
	}

	var missing []string
	if s.Runtime == "" {
		missing = append(missing, "runtime")
	}
	if s.Architecture == "" {
		missing = append(missing, "architecture")
	}
	if s.LayerArn == "" {
		missing = append(missing, "layerArn")
	}
	if len(missing) > 0 {
		return LambdaSettings{}, kerrors.Validation(
			"aws lambda compute is missing required settings: %v", missing)
	}
	return s, nil
}

func settingStr(settings map[string]any, key string) string {
	v, _ := settings[key].(string)
	return v
}

// settingInt reads an integer-valued setting. A manifest is YAML/JSON
// decoded, so a whole-number value normally arrives as int (YAML) or
// float64 (JSON/encoding-json round trips, e.g. via --set); both are
// accepted rather than forcing a manifest author to know which decoder
// produced the value they wrote.
func settingInt(settings map[string]any, key string, fallback int) int {
	switch v := settings[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return fallback
	}
}

// settingStrMap reads a string-to-string map setting (env/envSecrets),
// tolerating the map[string]any shape a YAML decoder produces for a nested
// mapping. Absent or wrongly-typed entries are dropped rather than failing
// the whole decode — an env var block is additive convenience, not
// something a manifest author cannot function without.
func settingStrMap(settings map[string]any, key string) map[string]string {
	raw, ok := settings[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
