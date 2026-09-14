// Package lockfile is the per-environment record of what `kraai up` actually
// provisioned, so `kraai down` can delete the union of what was created and
// what the current configuration declares.
//
// It exists because `down` once derived what to delete from the current
// configuration alone. A binding removed between an `up` and its matching
// `down` — which the GitHub Action's down-then-up re-run does on every push —
// was then never looked up and never deleted. The lock is the durable record
// that survives a configuration edit; merging it with the current declaration
// means neither side can regress: no lockfile falls back to configuration-only
// behavior, and an edited configuration cannot hide a resource the lock
// remembers.
//
// # Wire compatibility
//
// The JSON here is read and written by environments this tool did not create:
// a lockfile on disk may have been produced by the JavaScript CLI, and
// tearing those environments down is the whole point of reading it. Field
// names, field order, two-space indentation and the trailing newline all
// match that format exactly. `elVersion` keeps its pre-rename spelling for
// the same reason — it is a wire field, not a Go identifier, and renaming it
// would orphan every lockfile already on disk.
package lockfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/naming"
)

// Version is the only lockfileVersion this package understands.
const Version = 1

// dirName is the per-project directory lockfiles live in.
const dirName = ".kraai"

// Resource is one provisioned Cloudflare resource: the binding a service
// refers to it by, and the name it was actually created under.
type Resource struct {
	Binding string `json:"binding"`
	Name    string `json:"name"`
}

// Resources groups a service's provisioned resources by Cloudflare type.
type Resources struct {
	D1     []Resource `json:"d1"`
	KV     []Resource `json:"kv"`
	R2     []Resource `json:"r2"`
	Queues []Resource `json:"queues"`
}

// Service is one deployed worker's entry in the lock.
type Service struct {
	Dir                string    `json:"dir"`
	WorkerName         string    `json:"workerName"`
	WranglerVersion    string    `json:"wranglerVersion"`
	CompatibilityDate  string    `json:"compatibilityDate"`
	CompatibilityFlags []string  `json:"compatibilityFlags"`
	Resources          Resources `json:"resources"`
}

// Database records which provider provisioned the environment's database,
// alongside two opaque payloads.
//
// Options and Lock stay json.RawMessage on purpose: their shape belongs to
// the provider (Neon's Lock carries branchId/branchName/hyperdrive, another
// provider's would carry something else entirely), and a provider must be
// able to change its own payload without this package knowing. Decoding is
// the provider's job; carrying it intact is this package's.
type Database struct {
	Provider string          `json:"provider"`
	Options  json.RawMessage `json:"options"`
	Lock     json.RawMessage `json:"lock"`
}

// Lock is one environment's complete record. Field order is the serialized
// order — see the package comment on wire compatibility.
type Lock struct {
	LockfileVersion int    `json:"lockfileVersion"`
	Name            string `json:"name"`
	CreatedAt       string `json:"createdAt"`
	// ElVersion is the tool version that wrote this lock. The JSON key keeps
	// its pre-rename spelling deliberately; see the package comment.
	ElVersion string             `json:"elVersion"`
	AccountID string             `json:"accountId"`
	Subdomain string             `json:"subdomain"`
	Database  *Database          `json:"database"`
	Services  map[string]Service `json:"services"`
}

// Path is where the lock for an environment lives under dir.
//
// It validates name rather than trusting it. The name reaches here from a
// command line or, in the GitHub Action, from pull-request context, and
// filepath.Join cleans its argument — so an unvalidated "../../../etc/foo"
// would resolve cleanly outside the lock directory and Read would happily
// open it, or Write clobber it. naming.IsValidEnvironmentReference exists for
// exactly this ("safe to interpolate directly into a path keyed by
// environment name"); both grammars it accepts are [a-z0-9-] only, so a name
// that passes cannot contain a separator or a traversal segment.
func Path(dir, name string) (string, error) {
	if !naming.IsValidEnvironmentReference(name) {
		return "", kerrors.Validation(
			"%q is not a valid environment name, refusing to derive a lockfile path from it", name)
	}
	return filepath.Join(dir, dirName, name+".lock.json"), nil
}

// Empty is the initial shape Up writes before provisioning anything, so a run
// that fails immediately still leaves a lockfile naming the environment
// rather than nothing at all. Database and Services fill in as provisioning
// proceeds.
//
// Services is non-nil so it serializes as {} rather than null: a lockfile
// whose services key is null is not the shape the JavaScript CLI produces,
// and anything reading both must not have to handle two spellings of empty.
func Empty(name, version, accountID, subdomain string) *Lock {
	return &Lock{
		LockfileVersion: Version,
		Name:            name,
		CreatedAt:       time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		ElVersion:       version,
		AccountID:       accountID,
		Subdomain:       subdomain,
		Database:        nil,
		Services:        map[string]Service{},
	}
}

// Write persists lock, creating the lock directory on first write. It is safe
// to call repeatedly with a growing lock: Up calls it after every meaningful
// step so a partial run leaves an accurate partial record.
func Write(dir string, lock *Lock) error {
	if lock == nil {
		return kerrors.Validation("refusing to write a nil lock")
	}
	if lock.Name == "" {
		return kerrors.Validation("refusing to write a lock with no environment name")
	}
	file, err := Path(dir, lock.Name)
	if err != nil {
		return err
	}
	// 0700/0600: the lock records infrastructure identifiers for a live
	// environment - the Cloudflare account id, the provider's branch and
	// Hyperdrive ids, every provisioned resource name. It is written and read
	// by one user in their own project directory, and nothing else on the
	// machine has any reason to read it.
	if err := os.MkdirAll(filepath.Join(dir, dirName), 0o700); err != nil {
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "creating %s", filepath.Join(dir, dirName))
	}
	// Two-space indent plus a trailing newline, matching JSON.stringify(v,
	// null, 2) exactly — see the package comment.
	encoded, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "encoding lock for %q", lock.Name)
	}
	encoded = append(encoded, '\n')

	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "writing %s", file)
	}
	return nil
}

// Read returns the lock for an environment, or (nil, nil) when there is none
// — a pre-lockfile environment, or one already torn down.
//
// A file that exists but does not parse, or whose version is not Version, is
// an error rather than an absence. Treating a corrupt or future-version
// lockfile as missing would silently regress `down` to configuration-only
// deletion with nothing saying why, which is the exact failure this package
// exists to prevent.
func Read(dir, name string) (*Lock, error) {
	file, err := Path(dir, name)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(file) //nolint:gosec // Path validates name against naming.IsValidEnvironmentReference, which admits [a-z0-9-] only
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeUnexpected, "reading %s", file)
	}

	var lock Lock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return nil, kerrors.Wrap(err, kerrors.CodeValidation, "lockfile at %s is not valid JSON", file)
	}
	if lock.LockfileVersion != Version {
		return nil, kerrors.Validation(
			"lockfile at %s has lockfileVersion %d, expected %d — refusing to guess how to read it "+
				"rather than silently falling back to configuration-only deletion",
			file, lock.LockfileVersion, Version,
		)
	}
	if lock.Services == nil {
		lock.Services = map[string]Service{}
	}
	return &lock, nil
}

// Delete removes the lockfile if present. Already gone is success, not an
// error.
func Delete(dir, name string) error {
	file, err := Path(dir, name)
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return kerrors.Wrap(err, kerrors.CodeUnexpected, "removing %s", file)
	}
	return nil
}

// Bindings is the current-declaration side of a merge: the bindings a service
// declares now, by resource type.
//
// Bindings, not names. A declaration carries only the binding a service refers
// to a resource by; the name it was provisioned under is derived from the
// environment and service, which is why MergeResources needs both. Keeping
// this a plain string list rather than a manifest type keeps this package
// independent of whatever declares the bindings.
type Bindings struct {
	D1     []string
	KV     []string
	R2     []string
	Queues []string
}

// MergeResources returns the union of what the lock recorded as created and
// what the current declaration asks for, deduplicated by resource name within
// each type.
//
// This is the actual leak fix: a binding in the lock but no longer declared
// (removed between up and down) still appears, and a binding declared with no
// lock at all (a pre-lockfile environment) still appears too, so neither side
// of the merge can cause down to miss what the other would have caught.
//
// lockSvc and declared may each be nil, meaning that side contributes nothing.
func MergeResources(lockSvc *Service, declared *Bindings, serviceKey, environmentName string) Resources {
	var lockRes Resources
	if lockSvc != nil {
		lockRes = lockSvc.Resources
	}
	var d1, kv, r2, queues []string
	if declared != nil {
		d1, kv, r2, queues = declared.D1, declared.KV, declared.R2, declared.Queues
	}

	derive := func(bindings []string) []Resource {
		out := make([]Resource, 0, len(bindings))
		for _, binding := range bindings {
			out = append(out, Resource{
				Binding: binding,
				Name:    naming.ResourceName(environmentName, serviceKey, binding),
			})
		}
		return out
	}

	return Resources{
		D1:     mergeType(lockRes.D1, derive(d1)),
		KV:     mergeType(lockRes.KV, derive(kv)),
		R2:     mergeType(lockRes.R2, derive(r2)),
		Queues: mergeType(lockRes.Queues, derive(queues)),
	}
}

// mergeType deduplicates two resource lists by name, lock entries first.
//
// First-writer-wins on a name collision, matching the JavaScript it replaces:
// when the lock and the current declaration disagree about which binding a
// name belongs to, the lock's answer is the one that reflects what was
// actually provisioned. The result is always non-nil so it serializes as []
// rather than null.
func mergeType(lockEntries, declaredEntries []Resource) []Resource {
	seen := make(map[string]struct{}, len(lockEntries)+len(declaredEntries))
	out := make([]Resource, 0, len(lockEntries)+len(declaredEntries))
	for _, entry := range append(append([]Resource{}, lockEntries...), declaredEntries...) {
		if _, dup := seen[entry.Name]; dup {
			continue
		}
		seen[entry.Name] = struct{}{}
		out = append(out, entry)
	}
	return out
}
