// Package neonresource adapts the Neon management API to the resource
// contract, and provides the Hyperdrive configuration that fronts a branch.
//
// Both live here rather than beside their respective clients for the reason
// the Cloudflare adapters do: a client stays a client for one vendor's API,
// and the code that knows about kraai's contract sits above it. Hyperdrive is
// a Cloudflare resource but belongs to the Postgres capability, and its spec
// is derived entirely from the branch that precedes it — so it is adapted
// here, next to what produces its input, rather than among the storage types
// it has nothing to do with.
package neonresource

import (
	"context"

	"github.com/evatt-labs/kraai/internal/kerrors"
	"github.com/evatt-labs/kraai/internal/provider/neon"
	"github.com/evatt-labs/kraai/internal/resource"
)

// Provider and type names for the Neon side of the Postgres capability.
const (
	Provider   = "neon"
	TypeBranch = "branch"
	// Capability is what both types here fulfil.
	Capability = "postgres"
	// SecretConnectionURI is the credential a branch produces and the
	// Hyperdrive configuration consumes.
	SecretConnectionURI = "connection_uri"
)

// BranchSettings are the Neon-specific values a branch needs beyond its name.
//
// These have no home in the manifest schema today: a service declares
// databases: [{binding, engine}] and kraai.yaml names the vendor, but there
// is nowhere to say which Neon project to branch from or which role to
// connect as. Carried in Spec.Config for now, decoded here, so the shape is
// written down in one place when the manifest gains a provider-settings
// block.
type BranchSettings struct {
	// Project is the Neon project name to branch from.
	Project string
	// Database is the database within the branch to connect to.
	Database string
	// Role is the Postgres role to connect as.
	Role string
	// OrgID is optional. /projects refuses to list without an organization,
	// and a caller that has already resolved it skips a lookup by passing it.
	OrgID string
}

// decodeSettings reads BranchSettings out of a Spec's config.
func decodeSettings(config map[string]any) (BranchSettings, error) {
	s := BranchSettings{
		Project:  str(config, "project"),
		Database: str(config, "database"),
		Role:     str(config, "role"),
		OrgID:    str(config, "orgId"),
	}
	var missing []string
	if s.Project == "" {
		missing = append(missing, "project")
	}
	if s.Database == "" {
		missing = append(missing, "database")
	}
	if s.Role == "" {
		missing = append(missing, "role")
	}
	if len(missing) > 0 {
		return BranchSettings{}, kerrors.Validation(
			"neon branch is missing required settings: %v", missing)
	}
	return s, nil
}

func str(config map[string]any, key string) string {
	v, _ := config[key].(string)
	return v
}

// branchResource provisions a Neon branch per environment.
//
// A branch is a copy-on-write fork of the parent's data with its own compute
// endpoint, which is why Neon is the built-in: an environment gets a real
// database with real data in seconds, with no migration run — the thing D1
// needs a migrations pass to approximate.
type branchResource struct {
	client   *neon.Client
	settings BranchSettings
}

// resolveProject finds the project this branch lives in.
//
// Looked up on every verb rather than cached. It is one request, identity is
// never read from storage (D7), and a cache would have to be invalidated on
// exactly the event kraai cannot observe — someone renaming the project in
// Neon's console between two commands.
func (b *branchResource) resolveProject(ctx context.Context) (*neon.Project, error) {
	return b.client.FindProjectByName(ctx, b.settings.Project, b.settings.OrgID)
}

// Get reports the branch's state, or (nil, nil) when it does not exist.
//
// A missing *project* is an error rather than an absence: the project is
// configuration pointing at something that should already exist, so its
// absence is a misconfiguration, not a resource waiting to be created.
func (b *branchResource) Get(ctx context.Context, ref resource.Ref) (*resource.State, error) {
	project, err := b.resolveProject(ctx)
	if err != nil {
		return nil, err
	}
	branch, err := b.client.FindBranchByName(ctx, project.ID, ref.Name)
	if err != nil || branch == nil {
		return nil, err
	}
	return b.state(project, branch), nil
}

// Create forks the project's default branch.
func (b *branchResource) Create(ctx context.Context, spec resource.Spec) (*resource.State, error) {
	if spec.Name == "" {
		return nil, kerrors.Validation("cannot create a neon branch for binding %q without a derived name", spec.Binding)
	}
	project, err := b.resolveProject(ctx)
	if err != nil {
		return nil, err
	}
	parent, err := b.client.DefaultBranch(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	branch, err := b.client.CreateBranch(ctx, project.ID, parent.ID, spec.Name)
	if err != nil {
		return nil, err
	}
	return b.state(project, branch), nil
}

// Update is refused: a branch's identity is its name, and its content comes
// from the parent it forked. Changing either means a new branch.
func (b *branchResource) Update(context.Context, resource.Ref, resource.Spec) (*resource.State, error) {
	return nil, kerrors.Wrap(resource.ErrImmutable, kerrors.CodeValidation,
		"a neon branch is identified by its name and forked from its parent at creation")
}

// Delete removes the branch, treating an absent one as success.
func (b *branchResource) Delete(ctx context.Context, ref resource.Ref) error {
	project, err := b.resolveProject(ctx)
	if err != nil {
		return err
	}
	branch, err := b.client.FindBranchByName(ctx, project.ID, ref.Name)
	if err != nil {
		return err
	}
	if branch == nil {
		return nil
	}
	return b.client.DeleteBranch(ctx, project.ID, branch.ID)
}

// Secrets implements resource.SecretProducer: the branch's connection string
// is a live credential, so it is produced on demand rather than carried in
// the state that reaches the lockfile.
//
// Fetched fresh each call, which is also the only correct behaviour — Neon
// issues the URI against the branch's current compute endpoint, and a value
// captured earlier in a run is not guaranteed to still be the right one.
func (b *branchResource) Secrets(state *resource.State) map[string]resource.Secret {
	if state == nil {
		return nil
	}
	projectID := str(state.Attributes, "projectId")
	branchID := state.ID

	return map[string]resource.Secret{
		SecretConnectionURI: func(ctx context.Context) (string, error) {
			return b.client.ConnectionURI(ctx, projectID, neon.ConnectionOptions{
				BranchID: branchID,
				Database: b.settings.Database,
				Role:     b.settings.Role,
				// Direct endpoint, not the pooler: Hyperdrive pools in front
				// of this.
				Pooled: false,
			})
		},
	}
}

// state builds the State for a branch.
//
// Attributes carry what a later phase needs and nothing sensitive: the
// project and branch ids, the branch name, the database and role. The
// connection string is deliberately absent — it reaches Hyperdrive through
// Secrets, so nothing written to the lockfile has ever held it.
func (b *branchResource) state(project *neon.Project, branch *neon.Branch) *resource.State {
	return &resource.State{
		Ref: resource.Ref{Provider: Provider, Type: TypeBranch, Name: branch.Name},
		ID:  branch.ID,
		Attributes: map[string]any{
			"projectId":  project.ID,
			"branchId":   branch.ID,
			"branchName": branch.Name,
			"database":   b.settings.Database,
			"role":       b.settings.Role,
		},
	}
}
