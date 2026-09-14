package manifest_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/manifest"
)

func newRealLoader(t *testing.T, root string) *manifest.Loader {
	t.Helper()
	fsys := mustNewFS(t, root)
	return manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
}

// TestLoad_BlueprintExamplesParse is acceptance criterion 1: every example
// in docs/BLUEPRINT.md's "Manifest schema" section, copied verbatim into
// testdata/blueprint/, must parse into a fully resolved Manifest.
func TestLoad_BlueprintExamplesParse(t *testing.T) {
	loader := newRealLoader(t, "testdata/blueprint")

	got, err := loader.Load("prod", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Root.Version != 1 {
		t.Errorf("Root.Version = %d, want 1", got.Root.Version)
	}
	if got.Root.Providers.Compute == nil || got.Root.Providers.Compute.Vendor != "cloudflare" {
		t.Errorf("Root.Providers.Compute = %+v", got.Root.Providers.Compute)
	}
	if got.Root.Providers.Postgres == nil || got.Root.Providers.Postgres.Vendor != "neon" {
		t.Errorf("Root.Providers.Postgres = %+v", got.Root.Providers.Postgres)
	}
	// A vendor's own settings are carried through uninterpreted.
	if got := got.Root.Providers.Postgres.Settings["project"]; got != "kraai-control-plane" {
		t.Errorf("Postgres.Settings[project] = %v", got)
	}
	if got.Root.Hooks != "./kraai.hooks.mjs" {
		t.Errorf("Root.Hooks = %q", got.Root.Hooks)
	}
	if len(got.Root.Plugins) != 2 || got.Root.Plugins[1] != "./plugins/cost-guard.wasm" {
		t.Errorf("Root.Plugins = %v", got.Root.Plugins)
	}

	api, ok := got.Services["api"]
	if !ok {
		t.Fatalf("services = %v, want an %q entry", got.Services, "api")
	}
	if api.Dir != "packages/api" {
		t.Errorf("api.Dir = %q", api.Dir)
	}
	if len(api.Databases) != 2 {
		t.Fatalf("api.Databases = %+v", api.Databases)
	}
	if api.Databases[0].Binding != "DB" || api.Databases[0].Engine != "sqlite" {
		t.Errorf("Databases[0] = %+v", api.Databases[0])
	}
	pg := api.Databases[1]
	if pg.Binding != "PG" || pg.Engine != "postgres" {
		t.Errorf("Databases[1] = %+v", pg)
	}
	if pg.Caching == nil || pg.Caching.Disabled != false || pg.Caching.MaxAge != 60 {
		t.Errorf("Databases[1].Caching = %+v", pg.Caching)
	}
	if len(api.KeyValue) != 1 || api.KeyValue[0].Binding != "CACHE" {
		t.Errorf("api.KeyValue = %+v", api.KeyValue)
	}
	if len(api.Objects) != 1 || api.Objects[0].Binding != "ASSETS" {
		t.Errorf("api.Objects = %+v", api.Objects)
	}
	if len(api.Queues) != 1 || api.Queues[0].Binding != "JOBS" || !api.Queues[0].Consumer {
		t.Errorf("api.Queues = %+v", api.Queues)
	}

	env := got.Environment
	if env.Kind != manifest.EnvironmentKindPersistent {
		t.Errorf("Environment.Kind = %q", env.Kind)
	}
	if !env.Protected {
		t.Errorf("Environment.Protected = false, want true")
	}
	if env.Naming == nil || env.Naming.Prefix != "" {
		t.Errorf("Environment.Naming = %+v", env.Naming)
	}
	routes, ok := env.Routes["api"]
	if !ok || len(routes) != 1 || routes[0].Pattern != "api.acme.com" || !routes[0].CustomDomain {
		t.Errorf("Environment.Routes = %+v", env.Routes)
	}
	imports, ok := env.Resources["api"]
	if !ok {
		t.Fatalf("Environment.Resources = %+v, want an %q entry", env.Resources, "api")
	}
	if imports.Databases["DB"].ID != "0e1f...-uuid" {
		t.Errorf("imported DB ref = %+v", imports.Databases["DB"])
	}

	if got.Values["region"] != "enam" || got.Values["tier"] != "production" {
		t.Errorf("Values = %v", got.Values)
	}
}

func requireCode(t *testing.T, err error, code kerrors.Code) *kerrors.KError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error")
	}
	kerr, ok := err.(*kerrors.KError) //nolint:errorlint // asserting the concrete constructor return type is the point
	if !ok {
		t.Fatalf("expected *kerrors.KError, got %T (%v)", err, err)
	}
	if kerr.Code() != code {
		t.Fatalf("expected code %v, got %v", code, kerr.Code())
	}
	return kerr
}

// TestLoad_TemplateRenderErrorFailsLoudly is the first half of acceptance
// criterion 2: a .j2 file with a template error must fail at render time,
// not silently produce broken YAML that then fails (or worse, passes)
// schema validation.
func TestLoad_TemplateRenderErrorFailsLoudly(t *testing.T) {
	loader := newRealLoader(t, "testdata/render-error")

	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "kraai.yaml.j2") {
		t.Errorf("error %q does not name the failing template", kerr.Error())
	}
}

// TestLoad_RenderedButSchemaInvalidFailsValidation is the second half of
// acceptance criterion 2: a template that renders successfully but
// produces a document with an unknown field must fail with the same
// path-based error a hand-written file would produce.
func TestLoad_RenderedButSchemaInvalidFailsValidation(t *testing.T) {
	loader := newRealLoader(t, "testdata/schema-invalid-after-render")

	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "services.api: unknown field \"bogus\"") {
		t.Errorf("error %q does not contain the expected path-based message", kerr.Error())
	}
}

// TestLoad_SetOverridesValuesOverridesTemplateDefault is acceptance
// criterion 3: --set beats the values file, which beats a template's own
// default, in that order.
func TestLoad_SetOverridesValuesOverridesTemplateDefault(t *testing.T) {
	loader := newRealLoader(t, "testdata/precedence")

	t.Run("template default when neither values nor --set supply it", func(t *testing.T) {
		got, err := loader.Load("nodev", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Root.Providers.Compute == nil || got.Root.Providers.Compute.Vendor != "default-compute" {
			t.Errorf("Providers.Compute = %+v, want the template default", got.Root.Providers.Compute)
		}
	})

	t.Run("values file overrides the template default", func(t *testing.T) {
		got, err := loader.Load("dev", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Root.Providers.Compute == nil || got.Root.Providers.Compute.Vendor != "from-values" {
			t.Errorf("Providers.Compute = %+v, want the values-file value", got.Root.Providers.Compute)
		}
	})

	t.Run("--set overrides the values file", func(t *testing.T) {
		got, err := loader.Load("dev", []string{"compute=from-cli"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Root.Providers.Compute == nil || got.Root.Providers.Compute.Vendor != "from-cli" {
			t.Errorf("Providers.Compute = %+v, want the --set value", got.Root.Providers.Compute)
		}
	})
}

// TestLoad_RootTemplateRendersButFailsSchema is the root-file analogue of
// TestLoad_RenderedButSchemaInvalidFailsValidation: kraai.yaml.j2 itself
// (not a services file) renders successfully but the result has an
// unknown field.
func TestLoad_RootTemplateRendersButFailsSchema(t *testing.T) {
	loader := newRealLoader(t, "testdata/root-template-schema-invalid")
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "unknown field \"bogus\"") {
		t.Errorf("error %q does not name the unknown field", kerr.Error())
	}
}

// TestLoad_TemplatedServiceRendersSuccessfully proves a services/*.yaml.j2
// file that renders to valid, schema-conforming YAML loads normally —
// the success path alongside the render/schema failure paths covered
// elsewhere.
func TestLoad_TemplatedServiceRendersSuccessfully(t *testing.T) {
	loader := newRealLoader(t, "testdata/templated-service")
	got, err := loader.Load("dev", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	api, ok := got.Services["api"]
	if !ok || api.Dir != "packages/api" {
		t.Fatalf("services = %+v", got.Services)
	}
}

func TestLoad_MissingRootIsValidationError(t *testing.T) {
	loader := newRealLoader(t, "testdata/missing-root")
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_BothRootFilesIsValidationError(t *testing.T) {
	loader := newRealLoader(t, "testdata/both-root")
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "kraai.yaml") || !strings.Contains(kerr.Error(), "kraai.yaml.j2") {
		t.Errorf("error %q does not name both root files", kerr.Error())
	}
}

func TestLoad_UnknownTopLevelKeyRejected(t *testing.T) {
	loader := newRealLoader(t, "testdata/unknown-key")
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "unknown field \"bogus\"") {
		t.Errorf("error %q does not name the unknown field", kerr.Error())
	}
}

func TestLoad_UnknownNestedKeyRejectedWithPath(t *testing.T) {
	loader := newRealLoader(t, "testdata/unknown-nested-key")
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "services.api.databases[1].caching: unknown field \"maxage\"") {
		t.Errorf("error %q does not contain the expected dotted/bracketed path", kerr.Error())
	}
}

func TestLoad_DuplicateServiceAcrossFilesIsValidationError(t *testing.T) {
	loader := newRealLoader(t, "testdata/duplicate-service")
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "api") {
		t.Errorf("error %q does not name the duplicate service", kerr.Error())
	}
}

func TestLoad_MissingEnvironmentIsValidationError(t *testing.T) {
	loader := newRealLoader(t, "testdata/missing-environment")
	_, err := loader.Load("nope", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "nope") {
		t.Errorf("error %q does not name the missing environment", kerr.Error())
	}
}

func TestLoad_BadKindIsValidationError(t *testing.T) {
	loader := newRealLoader(t, "testdata/bad-kind")
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "kind") {
		t.Errorf("error %q does not mention kind", kerr.Error())
	}
}

func TestLoad_WrongVersionIsValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 2\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	kerr := requireCode(t, err, kerrors.CodeValidation)
	if !strings.Contains(kerr.Error(), "version") {
		t.Errorf("error %q does not mention version", kerr.Error())
	}
}

func TestLoad_TemplateRootReadErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return(nil, fsNotExistErr("kraai.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, errors.New("disk on fire"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_ServicesTemplateGlobErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 1\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))
	fsys.EXPECT().Glob("services/*.yaml").Return(nil, nil)
	fsys.EXPECT().Glob("services/*.yaml.j2").Return(nil, errors.New("glob exploded"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_BadSetArgPropagates(t *testing.T) {
	loader := newRealLoader(t, "testdata/blueprint")
	_, err := loader.Load("prod", []string{"nopequals"})
	_ = requireCode(t, err, kerrors.CodeValidation)
}

// The remaining tests use MockFS/MockTemplateEngine to reach loader.go
// branches a real fixture directory can't provoke on demand: a raw
// filesystem error (not "file doesn't exist") from Glob or ReadFile.

func TestLoad_ServicesGlobErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 1\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))
	fsys.EXPECT().Glob("services/*.yaml").Return(nil, errors.New("glob exploded"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_ServicesReadFileErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 1\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))
	fsys.EXPECT().Glob("services/*.yaml").Return([]string{"services/api.yaml"}, nil)
	fsys.EXPECT().Glob("services/*.yaml.j2").Return(nil, nil)
	fsys.EXPECT().ReadFile("services/api.yaml").Return(nil, errors.New("disk on fire"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_ServicesTemplateReadFileErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 1\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))
	fsys.EXPECT().Glob("services/*.yaml").Return(nil, nil)
	fsys.EXPECT().Glob("services/*.yaml.j2").Return([]string{"services/api.yaml.j2"}, nil)
	fsys.EXPECT().ReadFile("services/api.yaml.j2").Return(nil, errors.New("disk on fire"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_ServiceTemplateRenderErrorPropagates(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	tpl := manifest.NewMockTemplateEngine(ctrl)

	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 1\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))
	fsys.EXPECT().Glob("services/*.yaml").Return(nil, nil)
	fsys.EXPECT().Glob("services/*.yaml.j2").Return([]string{"services/api.yaml.j2"}, nil)
	fsys.EXPECT().ReadFile("services/api.yaml.j2").Return([]byte("services: {}"), nil)
	tpl.EXPECT().Render("services/api.yaml.j2", gomock.Any(), gomock.Any()).
		Return(nil, kerrors.Validation("services/api.yaml.j2: boom"))

	loader := manifest.NewLoader(fsys, tpl)
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_EnvironmentReadErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return([]byte("version: 1\n"), nil)
	fsys.EXPECT().ReadFile("kraai.yaml.j2").Return(nil, fsNotExistErr("kraai.yaml.j2"))
	fsys.EXPECT().Glob("services/*.yaml").Return(nil, nil)
	fsys.EXPECT().Glob("services/*.yaml.j2").Return(nil, nil)
	fsys.EXPECT().ReadFile("environments/dev.yaml").Return(nil, errors.New("disk on fire"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

func TestLoad_RootReadErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return(nil, fsNotExistErr("environments/dev.values.yaml"))
	fsys.EXPECT().ReadFile("kraai.yaml").Return(nil, errors.New("disk on fire"))

	loader := manifest.NewLoader(fsys, manifest.NewTemplateEngine(fsys))
	_, err := loader.Load("dev", nil)
	_ = requireCode(t, err, kerrors.CodeValidation)
}

// fsNotExistErr builds the same *fs.PathError shape a real FS
// implementation returns for a missing file, so mock-based tests exercise
// the errors.Is(err, fs.ErrNotExist) branch exactly as production code
// would.
func fsNotExistErr(name string) error {
	return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
