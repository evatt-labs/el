package manifest_test

import (
	"errors"
	"io/fs"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/evatt-labs/kraai/internal/manifest"
)

func TestLoadValues_MissingFileIsEmptyBase(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").
		Return(nil, &fs.PathError{Op: "open", Path: "environments/dev.values.yaml", Err: fs.ErrNotExist})

	got, err := manifest.LoadValues(fsys, "dev", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestLoadValues_ParsesFreeFormYAML(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/prod.values.yaml").
		Return([]byte("region: enam\ntier: production\n"), nil)

	got, err := manifest.LoadValues(fsys, "prod", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["region"] != "enam" || got["tier"] != "production" {
		t.Fatalf("got %v", got)
	}
}

func TestLoadValues_UnrecognizedReadErrorIsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").
		Return(nil, errors.New("disk on fire"))

	_, err := manifest.LoadValues(fsys, "dev", nil)
	if err == nil {
		t.Fatalf("expected an error")
	}
}

func TestLoadValues_InvalidYAMLIsValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").
		Return([]byte("not: [valid"), nil)

	_, err := manifest.LoadValues(fsys, "dev", nil)
	if err == nil {
		t.Fatalf("expected an error")
	}
}

func TestLoadValues_EmptyFileYieldsEmptyMap(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return([]byte(""), nil)

	got, err := manifest.LoadValues(fsys, "dev", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestLoadValues_SetOverridesValuesFile(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/prod.values.yaml").
		Return([]byte("tier: from-values\n"), nil)

	got, err := manifest.LoadValues(fsys, "prod", []string{"tier=from-cli"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["tier"] != "from-cli" {
		t.Fatalf("got %v, want tier=from-cli", got)
	}
}

func TestLoadValues_NullYAMLResetsToEmptyMap(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return([]byte("null\n"), nil)

	got, err := manifest.LoadValues(fsys, "dev", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestLoadValues_SetConflictAfterValuesFileIsValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").Return([]byte("tier: production\n"), nil)

	// tier is already a scalar from the values file; descending into it
	// with a further path segment must fail, not silently overwrite.
	_, err := manifest.LoadValues(fsys, "dev", []string{"tier.nested=x"})
	if err == nil {
		t.Fatalf("expected an error: %q is a scalar, not an object", "tier")
	}
}

func TestLoadValues_BadSetArgIsValidationError(t *testing.T) {
	ctrl := gomock.NewController(t)
	fsys := manifest.NewMockFS(ctrl)
	fsys.EXPECT().ReadFile("environments/dev.values.yaml").
		Return(nil, &fs.PathError{Op: "open", Path: "environments/dev.values.yaml", Err: fs.ErrNotExist})

	_, err := manifest.LoadValues(fsys, "dev", []string{"nopequals"})
	if err == nil {
		t.Fatalf("expected an error for a malformed --set")
	}
}
