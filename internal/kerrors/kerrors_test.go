package kerrors_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// exitCodeCases enumerates docs/BLUEPRINT.md D19's exit-code table for the
// error-producing buckets (0/success has no *KError and is covered
// separately in TestExitCode_NilIsSuccess). Both the constructor->Code
// mapping and the package-level ExitCode helper are asserted from this one
// table so the D19 table has exactly one source of truth in the test.
var exitCodeCases = []struct {
	name    string
	build   func() error
	code    kerrors.Code
	exit    int
	codeStr string
}{
	{
		name:    "generic/unexpected",
		build:   func() error { return kerrors.New("boom: %s", "widget") },
		code:    kerrors.CodeUnexpected,
		exit:    1,
		codeStr: "unexpected",
	},
	{
		name:    "validation",
		build:   func() error { return kerrors.Validation("field %q is required", "name") },
		code:    kerrors.CodeValidation,
		exit:    2,
		codeStr: "validation",
	},
	{
		name:    "lock held",
		build:   func() error { return kerrors.LockHeld("environment %q is locked by %s", "prod", "alice") },
		code:    kerrors.CodeLockHeld,
		exit:    3,
		codeStr: "lock-held",
	},
	{
		name:    "confirmation required",
		build:   func() error { return kerrors.ConfirmationRequired("confirmation %q did not match", "prod") },
		code:    kerrors.CodeConfirmationRequired,
		exit:    4,
		codeStr: "confirmation-required",
	},
}

func TestTypedConstructors_CodeAndExitCode(t *testing.T) {
	for _, tc := range exitCodeCases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.build()

			var kerr *kerrors.KError
			if !errors.As(err, &kerr) {
				t.Fatalf("errors.As(%v, *kerrors.KError) = false, want true", err)
			}

			if got := kerr.Code(); got != tc.code {
				t.Errorf("Code() = %v, want %v", got, tc.code)
			}
			if got := kerr.Code().ExitCode(); got != tc.exit {
				t.Errorf("Code().ExitCode() = %d, want %d", got, tc.exit)
			}
			if got := kerr.ExitCode(); got != tc.exit {
				t.Errorf("ExitCode() = %d, want %d", got, tc.exit)
			}
			if got := kerr.Code().String(); got != tc.codeStr {
				t.Errorf("Code().String() = %q, want %q", got, tc.codeStr)
			}
			if got := kerrors.ExitCode(err); got != tc.exit {
				t.Errorf("kerrors.ExitCode(err) = %d, want %d", got, tc.exit)
			}
		})
	}
}

func TestExitCode_NilIsSuccess(t *testing.T) {
	if got := kerrors.ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
}

func TestExitCode_UnrecognizedErrorFallsBackToUnexpected(t *testing.T) {
	plain := errors.New("some plain stdlib error")

	if got := kerrors.ExitCode(plain); got != kerrors.CodeUnexpected.ExitCode() {
		t.Errorf("ExitCode(plain) = %d, want %d (CodeUnexpected)", got, kerrors.CodeUnexpected.ExitCode())
	}
}

func TestExitCode_KErrorFoundDeepInChain(t *testing.T) {
	base := kerrors.LockHeld("environment %q is locked", "staging")
	wrapped := fmt.Errorf("apply failed: %w", base)

	if got := kerrors.ExitCode(wrapped); got != kerrors.CodeLockHeld.ExitCode() {
		t.Errorf("ExitCode(wrapped) = %d, want %d (CodeLockHeld)", got, kerrors.CodeLockHeld.ExitCode())
	}
}

func TestCode_UnknownStringsAreLabeled(t *testing.T) {
	var c kerrors.Code = 99
	if got, want := c.String(), "kerrors.Code(99)"; got != want {
		t.Errorf("Code(99).String() = %q, want %q", got, want)
	}
}

// TestKError_StdlibIsCompatible proves KError is drop-in compatible with
// stdlib errors.Is: a sentinel error wrapped via kerrors.Wrap must still be
// found by errors.Is walking KError's Unwrap chain.
func TestKError_StdlibIsCompatible(t *testing.T) {
	sentinel := errors.New("sentinel: lock file missing")
	kerr := kerrors.Wrap(sentinel, kerrors.CodeLockHeld, "acquiring lock for %s", "prod")

	if !errors.Is(kerr, sentinel) {
		t.Fatalf("errors.Is(kerr, sentinel) = false, want true")
	}
	if !errors.Is(fmt.Errorf("outer: %w", kerr), sentinel) {
		t.Fatalf("errors.Is(outer-wrapped kerr, sentinel) = false, want true")
	}
}

// customErr is a concrete error type used to prove errors.As can recover a
// type further down KError's Unwrap chain, not just *KError itself.
type customErr struct{ detail string }

func (e *customErr) Error() string { return "custom: " + e.detail }

func TestKError_StdlibAsCompatible(t *testing.T) {
	cause := &customErr{detail: "disk full"}
	kerr := kerrors.Wrap(cause, kerrors.CodeUnexpected, "writing state file")

	var got *customErr
	if !errors.As(kerr, &got) {
		t.Fatalf("errors.As(kerr, *customErr) = false, want true")
	}
	if got != cause {
		t.Errorf("errors.As recovered %+v, want the original %+v", got, cause)
	}
}

func TestKError_StdlibUnwrapCompatible(t *testing.T) {
	cause := errors.New("root cause")
	kerr := kerrors.Wrap(cause, kerrors.CodeValidation, "context")

	// Unwrap(kerr) yields KError's immediate cause (cockroachdb/errors'
	// own wrapper, message "context: root cause"); errors.Is/As above
	// already prove the chain continues down to the original cause. Here
	// we just confirm Unwrap is a real, one-level, stdlib-shaped step.
	unwrapped := errors.Unwrap(error(kerr))
	if unwrapped == nil {
		t.Fatalf("errors.Unwrap(kerr) = nil, want a non-nil cause")
	}
	if got, want := unwrapped.Error(), "context: root cause"; got != want {
		t.Errorf("errors.Unwrap(kerr).Error() = %q, want %q", got, want)
	}
	if !errors.Is(unwrapped, cause) {
		t.Errorf("errors.Is(errors.Unwrap(kerr), cause) = false, want true")
	}
}

func TestWrap_NilCauseReturnsNil(t *testing.T) {
	kerr := kerrors.Wrap(nil, kerrors.CodeValidation, "should not build")
	if kerr != nil {
		t.Errorf("Wrap(nil, ...) = %v, want nil", kerr)
	}
}

func TestWrap_MessageIsPrefixed(t *testing.T) {
	cause := errors.New("disk full")
	kerr := kerrors.Wrap(cause, kerrors.CodeUnexpected, "writing %s", "state.json")

	const want = "writing state.json: disk full"
	if got := kerr.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestFormat_PlusVIncludesStackNormalVDoesNot proves KError.Format defers
// to cockroachdb/errors' own formatting: %v/%s/Error() give only the
// message chain, while %+v additionally includes the captured stack trace
// (never hand-rolled — see the package doc comment).
func TestFormat_PlusVIncludesStackNormalVDoesNot(t *testing.T) {
	err := kerrors.Validation("bad value for %s", "region")

	msg := fmt.Sprintf("%v", err)
	stack := fmt.Sprintf("%+v", err)

	if msg != err.Error() {
		t.Errorf("%%v output %q != Error() %q", msg, err.Error())
	}
	if strings.Contains(msg, "kerrors_test.go") {
		t.Errorf("%%v output unexpectedly contains a stack frame: %q", msg)
	}
	if !strings.Contains(stack, "kerrors_test.go") {
		t.Errorf("%%+v output %q does not contain the expected stack frame (kerrors_test.go)", stack)
	}
	if !strings.HasPrefix(stack, err.Error()) {
		t.Errorf("%%+v output %q does not start with the message chain %q", stack, err.Error())
	}
}

func TestFormat_StackPointsAtCallSiteNotConstructor(t *testing.T) {
	err := kerrors.New("boom")

	stack := fmt.Sprintf("%+v", err)
	if strings.Contains(stack, "kerrors.go") {
		t.Errorf("%%+v output %q captured kerrors.go's own frame instead of skipping it", stack)
	}
}
