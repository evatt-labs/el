package manifest

import (
	"github.com/flosch/pongo2/v6"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

//go:generate go run go.uber.org/mock/mockgen -source=template.go -destination=mock_template_test.go -package=manifest

// TemplateEngine renders a Jinja2-style (D5) template. Kraai's chosen
// engine is pongo2 (D25); this interface exists so the loader's own tests
// never depend on pongo2's real syntax or behavior, and so a render
// failure can be simulated without constructing a template that's
// actually invalid pongo2.
type TemplateEngine interface {
	// Render renders template (the raw bytes of a .j2 file) against
	// context, returning the rendered output. source identifies the
	// template for error messages (typically its path within the
	// manifest). A syntax error or an execution-time error (an undefined
	// filter, a template calling something that errors) must both surface
	// here — rendering never partially succeeds silently.
	Render(source string, template []byte, context map[string]any) ([]byte, error)
}

// pongoEngine is TemplateEngine's real implementation, backed by pongo2
// v6.1.0 (D25 — decided, not re-litigated here).
type pongoEngine struct{}

// NewTemplateEngine returns pongo2-backed TemplateEngine.
func NewTemplateEngine() TemplateEngine {
	return pongoEngine{}
}

func (pongoEngine) Render(source string, template []byte, context map[string]any) ([]byte, error) {
	tpl, err := pongo2.FromBytes(template)
	if err != nil {
		return nil, kerrors.Validation("%s: template parse error: %v", source, err)
	}

	out, err := tpl.ExecuteBytes(pongo2.Context(context))
	if err != nil {
		return nil, kerrors.Validation("%s: template render error: %v", source, err)
	}

	return out, nil
}
