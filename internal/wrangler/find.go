package wrangler

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// binaryName is the executable to look for, which differs on Windows.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "wrangler.cmd"
	}
	return "wrangler"
}

// FindBinary locates a locally installed wrangler by walking up from startDir
// the way Node resolves node_modules.
//
// # It deliberately does not fall back to npx
//
// `npx wrangler` does not consult PATH. In a directory with no local install
// it silently downloads and runs the latest, unpinned, unverified wrangler
// from the registry — inside a process already holding Cloudflare and
// database credentials. A registry compromise of wrangler would land directly
// on those.
//
// Failing here with a clear message is the deliberate trade for that risk,
// not an oversight. Anything that "helpfully" adds a fallback reopens it.
func FindBinary(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", kerrors.Wrap(err, kerrors.CodeValidation, "resolving %s", startDir)
	}

	name := binaryName()
	for {
		candidate := filepath.Join(dir, "node_modules", ".bin", name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", kerrors.Validation(
				"could not find a locally-installed wrangler above %s — add wrangler as a "+
					"devDependency of this service or the workspace root; kraai will not fall "+
					"back to `npx wrangler`, which would run an unpinned version inside a "+
					"process holding your cloud credentials", startDir)
		}
		dir = parent
	}
}
