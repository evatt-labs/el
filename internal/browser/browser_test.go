package browser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckSafeRejectsNonHTTPSchemes(t *testing.T) {
	for _, target := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox",
		"ms-msdt:/id",
		"ftp://example.com/x",
	} {
		if err := CheckSafe(target); err == nil {
			t.Errorf("CheckSafe accepted %q", target)
		}
	}
}

func TestCheckSafeAcceptsHTTPAndHTTPS(t *testing.T) {
	for _, target := range []string{
		"http://example.com",
		"https://swift-blue-otter-12345.workers.dev/health",
		"https://example.com/path?a=1&b=2",
	} {
		if err := CheckSafe(target); err != nil {
			t.Errorf("CheckSafe rejected %q: %v", target, err)
		}
	}
}

// recordingOpener captures what would have been opened.
type recordingOpener struct {
	target string
	err    error
}

func (r *recordingOpener) Open(_ context.Context, target string) error {
	r.target = target
	return r.err
}

// TestOpenSwallowsFailures is the package's whole contract: a run must not
// fail provisioning because it could not open a tab.
func TestOpenSwallowsFailures(t *testing.T) {
	var warned []string
	Open(t.Context(), &recordingOpener{err: errors.New("no display")}, "https://example.com",
		func(format string, _ ...any) { warned = append(warned, format) })

	if len(warned) != 1 {
		t.Fatalf("got %d warnings, want exactly 1", len(warned))
	}
}

func TestOpenPassesTargetThrough(t *testing.T) {
	rec := &recordingOpener{}
	Open(t.Context(), rec, "https://example.com/health", nil)
	if rec.target != "https://example.com/health" {
		t.Fatalf("opened %q", rec.target)
	}
}

// TestShellMetacharactersSurviveAsData is the regression test for the WSL
// finding recorded in systemOpener.Open. `cmd.exe /c start` re-parses the
// string it is handed, so a URL ending ".../health&ver" ran `ver` as a second
// command — an argument vector does not help, because cmd.exe is a shell
// layer past where that guarantee applies.
//
// This asserts the payload is carried through as one opaque argument rather
// than being split or rejected: the scheme check deliberately does not try to
// filter metacharacters, since "&", "|" and "^" are all legal URL characters
// and a filter would both break real URLs and miss cases.
func TestShellMetacharactersSurviveAsData(t *testing.T) {
	const payload = "https://example.workers.dev/health&ver"

	if err := CheckSafe(payload); err != nil {
		t.Fatalf("a URL containing & is legal and must not be rejected: %v", err)
	}
	rec := &recordingOpener{}
	Open(t.Context(), rec, payload, nil)
	if rec.target != payload {
		t.Fatalf("target was altered in transit: %q", rec.target)
	}
	if strings.Count(rec.target, "&") != 1 {
		t.Fatalf("the metacharacter did not survive intact: %q", rec.target)
	}
}

// TestCheckSafeReturnsTheSentinels: the package documents ErrUnsafeScheme as
// what a caller branches on. Constructing a fresh error on each branch made
// errors.Is always false, so any caller relying on it took the wrong path
// silently.
func TestCheckSafeReturnsTheSentinels(t *testing.T) {
	err := CheckSafe("file:///etc/passwd")
	if !errors.Is(err, ErrUnsafeScheme) {
		t.Fatalf("got %v, which does not match ErrUnsafeScheme", err)
	}
	if !strings.Contains(err.Error(), "file") {
		t.Fatalf("the error should still name the offending scheme: %v", err)
	}

	if err := CheckSafe("https://ok.example.com"); err != nil {
		t.Fatalf("a valid URL was rejected: %v", err)
	}
}
