package lockfile

import (
	"regexp"
	"testing"
)

// TestCreatedAtMatchesJavaScriptFormat: the lockfile is read by both CLIs, and
// createdAt is the one field generated fresh rather than copied. Date's
// toISOString is always UTC with exactly three fractional digits and a Z.
func TestCreatedAtMatchesJavaScriptFormat(t *testing.T) {
	got := Empty("env-a", "1.0.0", "a", "s").CreatedAt
	want := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	if !want.MatchString(got) {
		t.Fatalf("createdAt = %q, which is not Date.toISOString()'s shape", got)
	}
}
