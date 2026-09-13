package naming

import (
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// TestResourceName_Golden pins legacy-node/test/names.test.mjs's
// resourceName fixtures.
func TestResourceName_Golden(t *testing.T) {
	cases := []struct {
		name, env, svc, binding, want string
	}{
		{
			name: "joins env, service key, and lowercased binding with hyphens",
			env:  "blue-honey-badger-12345", svc: "api", binding: "DB",
			want: "blue-honey-badger-12345-api-db",
		},
		{
			name: "replaces underscores and other non-alphanumerics with hyphens",
			env:  "n", svc: "api", binding: "MY_QUEUE",
			want: "n-api-my-queue",
		},
		{
			name: "collapses runs of separators rather than leaving them adjacent",
			env:  "n", svc: "api", binding: "MY__WEIRD--BINDING",
			want: "n-api-my-weird-binding",
		},
		{
			name: "strips a leading or trailing separator produced by the binding name itself",
			env:  "n", svc: "api", binding: "_LEADING",
			want: "n-api-leading",
		},
	}
	for _, c := range cases {
		got := ResourceName(c.env, c.svc, c.binding)
		if got != c.want {
			t.Errorf("%s: ResourceName(%q, %q, %q) = %q, want %q", c.name, c.env, c.svc, c.binding, got, c.want)
		}
	}
}

// TestResourceName_TruncatesTo63WithoutTrailingHyphen pins the JS
// suite's "truncates to 63 characters without leaving a trailing
// hyphen".
func TestResourceName_TruncatesTo63WithoutTrailingHyphen(t *testing.T) {
	long := ResourceName("n", "api", strings.Repeat("A", 80))
	if len(long) > 63 {
		t.Fatalf("len(%q) = %d, want <= 63", long, len(long))
	}
	if strings.HasSuffix(long, "-") {
		t.Fatalf("%q ends with a trailing hyphen", long)
	}
}

// TestResourceName_EmptySlugCanLeaveTrailingHyphenWhenUntruncated
// documents a real, inherited quirk found while porting: 0.5.0's
// trailing-hyphen strip only runs on the truncation branch
// (`name.slice(0, 63).replace(/-+$/, "")`), so a binding that slugs to
// the empty string (all separator characters) produces a name ending in
// a bare hyphen whenever the untruncated name is <= 63 bytes. This is
// not a bug introduced by the Go port — D22 freezes the behavior byte
// for byte — but it's worth having pinned down rather than accidentally
// "fixed" by a future edit.
func TestResourceName_EmptySlugCanLeaveTrailingHyphenWhenUntruncated(t *testing.T) {
	got := ResourceName("blue-honey-badger-12345", "api", "___")
	want := "blue-honey-badger-12345-api-"
	if got != want {
		t.Fatalf("ResourceName(..., \"___\") = %q, want %q", got, want)
	}
}

// TestResourceName_NonASCIIEnvironmentNameTruncatesByBytesNotRunes locks
// in the D22 UTF-16-vs-bytes divergence documented on ResourceName: a
// non-ASCII environmentName or serviceKey (never produced by either
// grammar this package owns, but not rejected by ResourceName itself
// either, since it doesn't re-validate its own arguments) is truncated
// by byte count, which can split a multi-byte rune. That's an accepted,
// deliberate consequence of choosing the 63-*byte* DNS/R2 limit over
// mirroring JS's UTF-16-code-unit slice/count — this test only pins down
// that it doesn't panic and still respects the 63-byte cap, not that the
// output is valid UTF-8.
func TestResourceName_NonASCIIEnvironmentNameTruncatesByBytesNotRunes(t *testing.T) {
	env := strings.Repeat("é", 40) // 2 bytes per rune in UTF-8: 80 bytes
	got := ResourceName(env, "a", "")
	if len(got) > 63 {
		t.Fatalf("len(%q) = %d, want <= 63", got, len(got))
	}
}

// slugPattern is slugify's own frozen output grammar: either empty, or
// one or more alphanumeric runs joined by single hyphens — never a
// leading/trailing hyphen, never a doubled hyphen, regardless of what
// nonsense the input binding contains.
var slugPattern = regexp.MustCompile(`^([a-z0-9]+(-[a-z0-9]+)*)?$`)

// TestRapid_Slugify_AlwaysMatchesFrozenPatternAndIsStable is D21's named
// rapid target for the binding-slugging half of naming derivation: for
// any input string whatsoever (empty, unicode, all separators, mixed
// case, arbitrarily long), slugify's output always matches slugPattern,
// and calling it twice on the same input always yields the same output.
func TestRapid_Slugify_AlwaysMatchesFrozenPatternAndIsStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s := rapid.String().Draw(t, "binding")

		got1 := slugify(s)
		got2 := slugify(s)
		if got1 != got2 {
			t.Fatalf("slugify(%q) not stable: %q vs %q", s, got1, got2)
		}
		if !slugPattern.MatchString(got1) {
			t.Fatalf("slugify(%q) = %q, does not match frozen slug pattern", s, got1)
		}
	})
}

// environmentNamePattern generates strings for the rapid tests below
// that actually satisfy NamePattern, so ResourceName is exercised with
// realistic environmentName inputs rather than arbitrary garbage that
// could never reach it in practice (D22's grammars are the only real
// source of environmentName values).
var environmentNamePattern = `[a-z]{2,15}-[a-z]{2,15}-[a-z]{2,15}-[0-9]{5}`

// serviceKeyPattern generates realistic manifest service keys: a
// services/*.yaml map key, which in every example in docs/BLUEPRINT.md
// is a short lowercase identifier.
var serviceKeyPattern = `[a-z][a-z0-9]{0,20}`

// TestRapid_ResourceName_BoundedAndStable is D21's named rapid target
// for resourceName's 63-byte bound and determinism guarantees,
// exercised over realistic environmentName/serviceKey values and fully
// arbitrary binding strings (to stress slugify).
func TestRapid_ResourceName_BoundedAndStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		env := rapid.StringMatching(environmentNamePattern).Draw(t, "env")
		svc := rapid.StringMatching(serviceKeyPattern).Draw(t, "svc")
		binding := rapid.String().Draw(t, "binding")

		got1 := ResourceName(env, svc, binding)
		got2 := ResourceName(env, svc, binding)
		if got1 != got2 {
			t.Fatalf("ResourceName(%q, %q, %q) not stable: %q vs %q", env, svc, binding, got1, got2)
		}

		if len(got1) > 63 {
			t.Fatalf("ResourceName(%q, %q, %q) = %q, len %d > 63", env, svc, binding, got1, len(got1))
		}

		untruncatedLen := len(env) + 1 + len(svc) + 1 + len(slugify(binding))
		if untruncatedLen > 63 && strings.HasSuffix(got1, "-") {
			t.Fatalf("ResourceName(%q, %q, %q) = %q, truncated but ends with a trailing hyphen", env, svc, binding, got1)
		}
	})
}
