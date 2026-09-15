package aws

import (
	"sort"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// validateKnownSettings rejects any key in a compute Spec's merged settings
// map that neither decodeLambdaSettings (lambdaSettingKeys,
// compute_settings.go) nor DecodeSettings (providerSettingKeys, settings.go)
// recognizes.
//
// # Why this exists
//
// providers.compute.settings is one free-form map (D34) read by two
// decoders in this package that know nothing about each other's
// vocabulary: DecodeSettings reads "region" for the AWS client,
// decodeLambdaSettings reads the Lambda-specific keys for the function's
// own desired state. Before this function existed, a manifest's
// `reservedConcurrency` (or any other typo — `reservdConcurrency`,
// `memory_size`) reached decodeLambdaSettings, matched none of its
// settingStr/settingInt calls, and was silently dropped: no error, no
// warning, no line in `kraai plan`'s output. That is the bug this
// workstream exists to fix (docs/BLUEPRINT.md's "no silent failures" rule)
// — a mistyped or unsupported setting must fail loudly, not do nothing.
//
// # Why the union lives here, and not inside either decoder
//
// Rejecting a key that decodeLambdaSettings alone did not recognize would
// reject "region" — the exact key DecodeSettings reads out of the same
// map. Rejecting a key DecodeSettings alone did not recognize would reject
// every Lambda-specific key. Neither decoder can safely validate against
// its own vocabulary alone, and neither should be made to import the
// other's key list directly: that would couple two decoders whose entire
// reason to be separate functions is that they read disjoint parts of the
// same map for unrelated purposes (an AWS SDK client vs. a Lambda function
// resource). validateKnownSettings is the third place, referenced by
// neither decoder's own package-level vocabulary, that composes
// lambdaSettingKeys and providerSettingKeys into the one union check a
// manifest's settings actually need to pass.
//
// # Why the check runs from decodeLambdaSettings, not DecodeSettings or
// internal/assemble
//
// Three places could plausibly run this check; two were rejected:
//
//   - internal/assemble, at registry-assembly time (aws.DecodeSettings is
//     already called there): rejected because assemble only ever sees
//     providers.compute.settings — the provider-level map, before
//     manifest.MergeSettings layers a service's own Compute.Settings over
//     it (internal/plan's expandCompute, out of this workstream's scope).
//     A typo made entirely inside one service's settings override would
//     never reach assemble at all, so validating there would miss it
//     outright, not just report it late.
//   - DecodeSettings itself: rejected for the same reason — it is called
//     once, at assemble time, against the pre-merge provider settings, and
//     structurally cannot see a per-service override key.
//
// decodeLambdaSettings is the one function actually reached, unconditionally,
// for every compute service on every `kraai plan`/`kraai apply` (lambda.go's
// DiffersFromState calls it before anything else, precisely so an invalid
// setting surfaces as a plan failure — see that method's own doc comment),
// and the settings map it receives is already the full post-merge map. It
// is the only one of the three call sites that can see everything a
// manifest author actually wrote for this vendor.
//
// # Alternative rejected: each decoder reports which keys it consumed
//
// Rather than a static declared list, decodeLambdaSettings and
// DecodeSettings could each track which keys they actually read during
// decoding and return that set for a caller to reconcile against the input
// map's keys. Rejected: it requires every settingStr/settingInt/
// settingStrMap call site to also record its own key as a side effect,
// which is easy to forget when a field is added (silently reintroducing
// this exact bug for the new field) and impossible to get right when a
// decoder returns early on a validation error before reading every field —
// decodeLambdaSettings already does this for missing runtime/architecture/
// layerArn. A static declared list checked before any decoding happens
// reports every offending key in one pass, on the first run, rather than
// one field at a time across repeated fixes.
func validateKnownSettings(settings map[string]any) error {
	known := make(map[string]struct{}, len(lambdaSettingKeys)+len(providerSettingKeys))
	for _, k := range lambdaSettingKeys {
		known[k] = struct{}{}
	}
	for _, k := range providerSettingKeys {
		known[k] = struct{}{}
	}

	var unknown []string
	for k := range settings {
		if _, ok := known[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown) // deterministic error message across map iteration order

	allKnown := make([]string, 0, len(known))
	for k := range known {
		allKnown = append(allKnown, k)
	}
	sort.Strings(allKnown)

	msgs := make([]string, len(unknown))
	for i, k := range unknown {
		if match := closestKey(k, allKnown); match != "" {
			msgs[i] = k + " (did you mean " + match + "?)"
		} else {
			msgs[i] = k
		}
	}
	return kerrors.Validation(
		"aws provider settings: unrecognized key(s): %s — recognized keys: %s",
		strings.Join(msgs, ", "), strings.Join(allKnown, ", "))
}

// closestKey returns the entry in candidates within Levenshtein distance 2
// of key, preferring the nearest; empty when nothing is close enough to be
// a plausible typo suggestion rather than noise for a key that is simply
// not supported at all (e.g. "package" spelled correctly but naming a
// concept this vendor's Lambda function has no such setting for).
func closestKey(key string, candidates []string) string {
	const maxSuggestDistance = 2
	best := ""
	bestDist := maxSuggestDistance + 1
	for _, c := range candidates {
		d := levenshtein(key, c)
		if d < bestDist {
			bestDist = d
			best = c
		}
	}
	if bestDist > maxSuggestDistance {
		return ""
	}
	return best
}

// levenshtein returns the edit distance between a and b (single-character
// insert/delete/substitute), via the standard O(len(a)*len(b)) dynamic
// program. Hand-rolled rather than a dependency: this is the entire
// algorithm, used for one purpose (a short "did you mean" suggestion on a
// validation error), and not worth a third-party import for.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
