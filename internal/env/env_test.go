package env

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDotEnv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The .env body and expectations below carry fabricated credentials; a loader
// for credential files cannot be tested without them.
//
//nolint:gosec // G101: fabricated test fixtures
func TestLoadDotEnv(t *testing.T) {
	dir := writeDotEnv(t, strings.Join([]string{
		"# a comment",
		"",
		"PLAIN=value",
		"  SPACED = spaced-key-is-not-trimmed ",
		`QUOTED="has spaces and a # hash"`,
		`ESCAPED="say \"hi\""`,
		"NO_EQUALS_SIGN",
		"EMPTY=",
		"URL=postgres://u:p@h/d?sslmode=require",
	}, "\n"))

	if err := LoadDotEnv(dir); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}

	checks := map[string]string{
		"PLAIN":   "value",
		"QUOTED":  "has spaces and a # hash",
		"ESCAPED": `say "hi"`,
		"URL":     "postgres://u:p@h/d?sslmode=require",
	}
	for key, want := range checks {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// "=" inside a value must not split the line a second time.
	if strings.Count(os.Getenv("URL"), "=") == 0 {
		t.Error("URL lost everything after its first = sign")
	}
}

// TestLoadDotEnvDoesNotOverrideExported is the `??=` semantic: an exported
// variable, or one injected by CI, is a deliberate act that a file on disk
// must not silently override.
func TestLoadDotEnvDoesNotOverrideExported(t *testing.T) {
	dir := writeDotEnv(t, "TOKEN=from-file\n")
	t.Setenv("TOKEN", "from-environment")

	if err := LoadDotEnv(dir); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("TOKEN"); got != "from-environment" {
		t.Fatalf("TOKEN = %q — the file overrode an explicitly exported value", got)
	}
}

// TestLoadDotEnvFillsAnEmptyExportedValue pins the deliberate divergence from
// the JavaScript's `??=`. An unset CI secret expands to the empty string, and
// the JavaScript would leave it empty and then fail Require, which counts
// empty as missing — the two halves disagreeing about what "set" means, in
// exactly the case where it matters.
func TestLoadDotEnvFillsAnEmptyExportedValue(t *testing.T) {
	dir := writeDotEnv(t, "TOKEN=from-file\n")
	t.Setenv("TOKEN", "")

	if err := LoadDotEnv(dir); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("TOKEN"); got != "from-file" {
		t.Fatalf("TOKEN = %q, want the .env value to fill an empty export", got)
	}
	// And the two halves must now agree.
	if _, err := Require("TOKEN"); err != nil {
		t.Fatalf("Require rejected what LoadDotEnv just filled: %v", err)
	}
}

// A missing .env is a convenience not being used, not a failure: CI exports
// its variables instead.
func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	if err := LoadDotEnv(t.TempDir()); err != nil {
		t.Fatalf("a missing .env should not be an error, got %v", err)
	}
}

func TestRequire(t *testing.T) {
	t.Setenv("PRESENT_A", "a")
	t.Setenv("PRESENT_B", "b")

	got, err := Require("PRESENT_A", "PRESENT_B")
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if got["PRESENT_A"] != "a" || got["PRESENT_B"] != "b" {
		t.Fatalf("got %v", got)
	}
}

// Every missing key at once, so a misconfigured environment takes one run to
// diagnose rather than one run per variable.
func TestRequireNamesAllMissingKeys(t *testing.T) {
	t.Setenv("PRESENT", "x")
	t.Setenv("MISSING_ONE", "")
	t.Setenv("MISSING_TWO", "")

	_, err := Require("PRESENT", "MISSING_ONE", "MISSING_TWO")
	if err == nil {
		t.Fatal("expected an error for missing variables")
	}
	for _, key := range []string{"MISSING_ONE", "MISSING_TWO"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error did not name %s: %v", key, err)
		}
	}
}

// TestRequireNeverEchoesValues is the property the package exists for: the
// error names keys, never the credentials behind them.
//
//nolint:gosec // G101: a deliberately fake secret, which is the point of the test
func TestRequireNeverEchoesValues(t *testing.T) {
	const secret = "sk-live-do-not-log-this"
	t.Setenv("PRESENT_SECRET", secret)
	t.Setenv("ABSENT", "")

	_, err := Require("PRESENT_SECRET", "ABSENT")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error echoed a credential: %v", err)
	}
}

// An empty value is missing, not present: an exported-but-blank variable is
// the classic CI misconfiguration, and treating it as set means failing
// later, further from the cause.
func TestRequireTreatsEmptyAsMissing(t *testing.T) {
	t.Setenv("BLANK", "")
	if _, err := Require("BLANK"); err == nil {
		t.Fatal("an empty value was accepted as present")
	}
}

// TestLoadDotEnvTrimsAroundTheSeparator: "API_TOKEN = abc" is a shape people
// write. Untrimmed it defines a variable literally named "API_TOKEN " — legal
// in setenv, invisible in a diff — which Require then reports missing while
// the file plainly contains it.
func TestLoadDotEnvTrimsAroundTheSeparator(t *testing.T) {
	dir := writeDotEnv(t, "SPACED_KEY = spaced-value\nTABBED\t=\tvalue2\n")
	t.Setenv("SPACED_KEY", "")
	t.Setenv("TABBED", "")

	if err := LoadDotEnv(dir); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if _, err := Require("SPACED_KEY", "TABBED"); err != nil {
		t.Fatalf("Require rejected variables the file defines: %v", err)
	}
	if got := os.Getenv("SPACED_KEY"); got != "spaced-value" {
		t.Fatalf("SPACED_KEY = %q, want no surrounding whitespace", got)
	}
}

// TestLoadDotEnvSkipsKeylessLines: os.Setenv rejects an empty key, so a stray
// "=oops" would fail the whole load and stop every command from starting.
// Skipping is the documented contract for a line carrying no assignment.
func TestLoadDotEnvSkipsKeylessLines(t *testing.T) {
	dir := writeDotEnv(t, "=oops\n   =also-oops\nGOOD=value\n")
	t.Setenv("GOOD", "")

	if err := LoadDotEnv(dir); err != nil {
		t.Fatalf("a stray line should be skipped, not fail the load: %v", err)
	}
	if got := os.Getenv("GOOD"); got != "value" {
		t.Fatalf("GOOD = %q — parsing stopped at the stray line", got)
	}
}
