package naming

import (
	"regexp"
	"strings"
)

// nonAlnumRun matches one or more consecutive characters outside
// [a-z0-9], on an already-lowercased string — the separator run
// slugify collapses to a single hyphen.
var nonAlnumRun = regexp.MustCompile(`[^a-z0-9]+`)

// slugify normalizes s into the form ResourceName uses for its
// binding-derived path segment: lowercase, every run of
// non-alphanumeric characters collapsed to one hyphen, then any leading
// or trailing hyphen stripped. Byte-for-byte 0.5.0's
// binding.toLowerCase().replaceAll(/[^a-z0-9]+/g,
// "-").replaceAll(/^-+|-+$/g, "") (D22).
func slugify(s string) string {
	return strings.Trim(nonAlnumRun.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// ResourceName builds the name kraai provisions for one binding —
// {environmentName}-{serviceKey}-{slug(binding)} — byte-for-byte 0.5.0's
// resourceName (D22). R2 bucket names specifically must be lowercase,
// DNS-compliant, and 63 characters or fewer; this satisfies that for
// every resource type rather than having per-type naming rules drift
// apart, since a binding name like MY_QUEUE is common and would
// otherwise produce an invalid bucket name.
//
// Only binding is slugged. environmentName and serviceKey are
// interpolated raw, matching 0.5.0 exactly — slugging them too would
// change every name already deployed by a 0.4.x/0.5.x manifest that
// used them verbatim.
//
// Truncation is byte-length, not rune-length: 0.5.0's `name.slice(0,
// 63)` counts UTF-16 code units, which only diverges from a Go
// byte-length slice for a non-ASCII environmentName or serviceKey
// (binding is always ASCII after slugify). Byte-length is the correct
// choice on its own merits, independent of the divergence: 63 *bytes*
// is the actual DNS/R2 constraint this exists to satisfy, not 63 UTF-16
// code units, which was only ever an artifact of the host language. A
// non-ASCII environmentName/serviceKey is not a live input today
// (D22's grammars are both lowercase ASCII-only), so the divergence is
// theoretical, not observed in practice.
//
// The trailing-hyphen strip below runs only on the truncation branch,
// matching 0.5.0's `name.slice(0, 63).replace(/-+$/, "")` exactly: an
// untruncated name that happens to end in a hyphen — only reachable via
// a binding that slugs to the empty string, e.g. "___" or "" — is
// returned as-is, hyphen and all. That's an inherited quirk from the JS
// original, not something this port introduces or should "fix" (D22
// freezes behavior); see resource_test.go for the case pinned down.
func ResourceName(environmentName, serviceKey, binding string) string {
	slug := slugify(binding)
	name := environmentName + "-" + serviceKey + "-" + slug
	if len(name) <= 63 {
		return name
	}
	return strings.TrimRight(name[:63], "-")
}
