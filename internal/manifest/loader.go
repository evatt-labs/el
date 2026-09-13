package manifest

import (
	"errors"
	"io/fs"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

const (
	rootFile        = "kraai.yaml"
	rootTemplate    = "kraai.yaml.j2"
	servicesGlob    = "services/*.yaml"
	servicesJ2Glob  = "services/*.yaml.j2"
	environmentsDir = "environments"
)

// Loader resolves a manifest directory (D4) into one validated Manifest.
// Both external systems it touches — the filesystem and the template
// engine — are injected interfaces (FS, TemplateEngine), so Loader itself
// never imports os or pongo2 directly.
type Loader struct {
	fs       FS
	template TemplateEngine
}

// NewLoader builds a Loader reading from fsys and rendering .j2 files with
// engine.
func NewLoader(fsys FS, engine TemplateEngine) *Loader {
	return &Loader{fs: fsys, template: engine}
}

// Load resolves the manifest for envName: kraai.yaml (+ services/*.yaml,
// merged per D4) rendered opt-in-by-extension (D5) against the merged
// values (environments/<envName>.values.yaml + setArgs, Helm precedence),
// then the environment overlay itself — every schema-validated file
// strictly rejecting unknown keys along the way.
func (l *Loader) Load(envName string, setArgs []string) (*Manifest, error) {
	values, err := LoadValues(l.fs, envName, setArgs)
	if err != nil {
		return nil, err
	}

	root, err := l.loadRoot(values)
	if err != nil {
		return nil, err
	}

	services, err := l.loadServices(values)
	if err != nil {
		return nil, err
	}

	env, err := l.loadEnvironment(envName)
	if err != nil {
		return nil, err
	}

	return &Manifest{
		Root:        *root,
		Services:    services,
		Environment: *env,
		Values:      values,
	}, nil
}

// loadRoot loads kraai.yaml or kraai.yaml.j2 — exactly one must exist.
func (l *Loader) loadRoot(values map[string]any) (*Root, error) {
	plainData, plainErr := l.readOptional(rootFile)
	if plainErr != nil {
		return nil, plainErr
	}
	tplData, tplErr := l.readOptional(rootTemplate)
	if tplErr != nil {
		return nil, tplErr
	}

	switch {
	case plainData != nil && tplData != nil:
		return nil, kerrors.Validation(
			"both %s and %s exist; a manifest root may only have one", rootFile, rootTemplate)
	case plainData != nil:
		var root Root
		if err := DecodeStrict(plainData, rootFile, &root); err != nil {
			return nil, err
		}
		return &root, validateRoot(&root)
	case tplData != nil:
		rendered, err := l.template.Render(rootTemplate, tplData, values)
		if err != nil {
			return nil, err
		}
		var root Root
		if err := DecodeStrict(rendered, rootTemplate, &root); err != nil {
			return nil, err
		}
		return &root, validateRoot(&root)
	default:
		return nil, kerrors.Validation("%s is required (or %s)", rootFile, rootTemplate)
	}
}

func validateRoot(root *Root) error {
	if root.Version != 1 {
		return kerrors.Validation("%s: version: must be 1, got %d", rootFile, root.Version)
	}
	return nil
}

// loadServices globs services/*.yaml and services/*.yaml.j2, renders the
// latter against values, strictly decodes both, and merges them (D4).
func (l *Loader) loadServices(values map[string]any) (map[string]Service, error) {
	plainMatches, err := l.fs.Glob(servicesGlob)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "globbing %s", servicesGlob)
	}
	tplMatches, err := l.fs.Glob(servicesJ2Glob)
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "globbing %s", servicesJ2Glob)
	}

	var files []ServicesFile
	var sources []string

	for _, name := range plainMatches {
		data, err := l.fs.ReadFile(name)
		if err != nil {
			return nil, kerrors.Wrap(err, kerrors.CodeValidation, "reading %s", name)
		}
		var file ServicesFile
		if err := DecodeStrict(data, name, &file); err != nil {
			return nil, err
		}
		files = append(files, file)
		sources = append(sources, name)
	}

	for _, name := range tplMatches {
		data, err := l.fs.ReadFile(name)
		if err != nil {
			return nil, kerrors.Wrap(err, kerrors.CodeValidation, "reading %s", name)
		}
		rendered, err := l.template.Render(name, data, values)
		if err != nil {
			return nil, err
		}
		var file ServicesFile
		if err := DecodeStrict(rendered, name, &file); err != nil {
			return nil, err
		}
		files = append(files, file)
		sources = append(sources, name)
	}

	return mergeServiceFiles(files, sources)
}

// loadEnvironment loads environments/<envName>.yaml, the schema-validated,
// never-templated overlay (D5 names only kraai.yaml.j2 and
// services/*.yaml.j2 as opt-in-templated).
func (l *Loader) loadEnvironment(envName string) (*Environment, error) {
	path := environmentsDir + "/" + envName + ".yaml"
	data, err := l.fs.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, kerrors.Validation("%s: environment %q not found", path, envName)
		}
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "reading %s", path)
	}

	var env Environment
	if err := DecodeStrict(data, path, &env); err != nil {
		return nil, err
	}
	if err := validateEnvironment(path, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func validateEnvironment(path string, env *Environment) error {
	switch env.Kind {
	case EnvironmentKindEphemeral, EnvironmentKindPersistent:
		return nil
	default:
		return kerrors.Validation("%s: kind: must be %q or %q, got %q",
			path, EnvironmentKindEphemeral, EnvironmentKindPersistent, env.Kind)
	}
}

// readOptional reads name, returning (nil, nil) if it doesn't exist rather
// than an error — used for kraai.yaml/kraai.yaml.j2, where "doesn't exist"
// is one expected branch, not a failure, until both are checked together.
func (l *Loader) readOptional(name string) ([]byte, error) {
	data, err := l.fs.ReadFile(name)
	if err == nil {
		return data, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return nil, kerrors.Wrap(err, kerrors.CodeValidation, "reading %s", name)
}
