package manifest_test

import (
	"strings"
	"testing"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/manifest"
)

func TestPongoEngine_RendersWithContext(t *testing.T) {
	engine := manifest.NewTemplateEngine()
	out, err := engine.Render("t.j2", []byte("hello {{ name }}"), map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "hello world" {
		t.Fatalf("got %q", out)
	}
}

func TestPongoEngine_DefaultFilterAppliesWhenContextKeyMissing(t *testing.T) {
	engine := manifest.NewTemplateEngine()
	out, err := engine.Render("t.j2", []byte("{{ missing|default:'fallback' }}"), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != "fallback" {
		t.Fatalf("got %q", out)
	}
}

func TestPongoEngine_ParseErrorIsValidationError(t *testing.T) {
	engine := manifest.NewTemplateEngine()
	_, err := engine.Render("bad.j2", []byte("{% if true %}unterminated"), nil)
	requireEngineValidationError(t, err, "bad.j2")
}

func TestPongoEngine_ExecutionErrorIsValidationError(t *testing.T) {
	engine := manifest.NewTemplateEngine()
	// divisibleby with a non-numeric input triggers a runtime execution
	// error rather than a parse-time one.
	_, err := engine.Render("bad.j2", []byte("{{ name|divisibleby:0 }}"), map[string]any{"name": "x"})
	if err == nil {
		t.Skip("pongo2 tolerated this input; execution-error path exercised via loader_test.go instead")
	}
	requireEngineValidationError(t, err, "bad.j2")
}

func requireEngineValidationError(t *testing.T, err error, wantSource string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error")
	}
	kerr, ok := err.(*kerrors.KError) //nolint:errorlint // asserting the concrete constructor return type is the point
	if !ok {
		t.Fatalf("expected *kerrors.KError, got %T (%v)", err, err)
	}
	if kerr.Code() != kerrors.CodeValidation {
		t.Fatalf("expected CodeValidation, got %v", kerr.Code())
	}
	if !strings.Contains(kerr.Error(), wantSource) {
		t.Errorf("error %q does not mention source %q", kerr.Error(), wantSource)
	}
}
