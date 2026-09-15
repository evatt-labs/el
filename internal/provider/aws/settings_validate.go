package aws

import (
	"sort"
	"strings"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// validateKnownSettings rejects any key in a compute Spec's merged settings
// map that no reader of that map — decodeLambdaSettings
// (lambdaSettingKeys, compute_settings.go), DecodeSettings
// (providerSettingKeys, settings.go), or lambdaURLResource.translate
// (lambdaURLSettingKeys, lambdaurl.go) — recognizes.
//
// # Why this exists
//
// providers.compute.settings is one free-form map (D34) read by three
// places in this package that know nothing about each other's vocabulary:
// DecodeSettings reads "region" for the AWS client, decodeLambdaSettings
// reads the Lambda-function-specific keys, and lambdaURLResource.translate
// reads "functionUrlAuthType" for the function's direct invoke URL. Before
// this function existed, a manifest's `reservedConcurrency` (or any other
// typo — `reservdConcurrency`, `memory_size`) reached decodeLambdaSettings,
// matched none of its settingStr/settingInt calls, and was silently
// dropped: no error, no warning, no line in `kraai plan`'s output. That is
// the bug this workstream exists to fix (docs/BLUEPRINT.md's "no silent
// failures" rule) — a mistyped or unsupported setting must fail loudly,
// not do nothing. The third reader (lambdaURLSettingKeys) was found only
// by building this validator: functionUrlAuthType is a real,
// already-working setting this validator would otherwise have rejected as
// unrecognized the moment a manifest used it, the identical failure mode
// one key over — proof that a union check needs every real reader
// accounted for, not just the two the original bug report named.
//
// # Why the union lives here, and not inside any one reader
//
// Rejecting a key that decodeLambdaSettings alone did not recognize would
// reject "region" and "functionUrlAuthType" — keys the other two readers
// use out of the same map. The same is true symmetrically for each of the
// other two. No single reader can safely validate against its own
// vocabulary alone, and none should be made to import another's key list
// directly: that would couple readers whose entire reason to be separate
// functions is that they read disjoint parts of the same map for
// unrelated purposes (an AWS SDK client, a Lambda function's desired
// state, a Lambda function URL's desired state). validateKnownSettings is
// the one place, referenced by none of the three readers' own
// package-level vocabulary, that composes lambdaSettingKeys,
// providerSettingKeys and lambdaURLSettingKeys into the union a manifest's
// settings actually need to pass.
//
// # Why the check must run unconditionally, for every planned action
//
// This function itself is pure and side-effect-free regardless of where
// it is called from; where it is reachable from is what a real
// `kraai plan` against a brand-new environment exposed as broken. It used
// to be reached only via decodeLambdaSettings being called from
// lambda.go's DiffersFromState — and DiffersFromState only ever runs when
// internal/plan's decide found an existing resource to compare against
// (see resource.Resource's Get contract). A first-ever plan against a
// fresh environment gives every resource ActionCreate, DiffersFromState is
// never reached for any of them, and a typo'd or bogus setting reached
// nothing at all — exactly the silent failure this workstream exists to
// close, just moved one layer down instead of fixed. Verified against the
// real kraai-api manifest: `reservedConcurency` (typo) and
// `httpFrontDoor: totally-bogus-value` both planned clean, no error, on a
// fresh environment.
//
// The fix is not another place inside this package to hang the check off
// of — decodeLambdaSettings is still called from exactly where it always
// was, unchanged in this file. It is internal/plan's decide (planner.go)
// now calling lambdaFunctionResource.ValidateSpec (lambda.go), which
// decodeLambdaSettings backs, through the optional plan.SpecValidator
// interface, unconditionally, before it ever branches on whether the
// resource already exists. See plan.SpecValidator's own doc comment for
// why that call site — not internal/assemble, not DecodeSettings itself —
// is the correct one, and for the alternatives rejected there.
//
// # Alternative rejected: each reader reports which keys it consumed
//
// Rather than a static declared list, each reader could track which keys
// it actually read during decoding and return that set for a caller to
// reconcile against the input map's keys. Rejected: it requires every
// settingStr/settingInt/settingStrMap call site to also record its own
// key as a side effect, which is easy to forget when a field is added
// (silently reintroducing this exact bug for the new field — precisely
// how lambdaURLSettingKeys almost did not exist, until this file's own
// union check was built and functionUrlAuthType had to be accounted for
// by hand) and impossible to get right when a decoder returns early on a
// validation error before reading every field — decodeLambdaSettings
// already does this for missing runtime/architecture/layerArn. A static
// declared list checked before any decoding happens reports every
// offending key in one pass, on the first run, rather than one field at a
// time across repeated fixes.
func validateKnownSettings(settings map[string]any) error {
	known := make(map[string]struct{}, len(lambdaSettingKeys)+len(providerSettingKeys)+len(lambdaURLSettingKeys))
	for _, k := range lambdaSettingKeys {
		known[k] = struct{}{}
	}
	for _, k := range providerSettingKeys {
		known[k] = struct{}{}
	}
	for _, k := range lambdaURLSettingKeys {
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
