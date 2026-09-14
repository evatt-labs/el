package neon

import (
	"context"
	"net/url"
	"strconv"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// branchesPerPage is well under the endpoint's 10000 ceiling but far above
// any project kraai operates on, so a walk is almost always one request.
const branchesPerPage = 1000

// Branch is a Neon branch.
type Branch struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Default  bool   `json:"default"`
	ParentID string `json:"parent_id"`
}

// branchPage is one page of a branches listing.
type branchPage struct {
	Branches   []Branch   `json:"branches"`
	Pagination pagination `json:"pagination"`
}

// listBranches walks every branch in a project.
func (c *Client) listBranches(ctx context.Context, projectID string) ([]Branch, error) {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(branchesPerPage))
	return listCursor(ctx, c, "/projects/"+url.PathEscape(projectID)+"/branches", query,
		func(p branchPage) ([]Branch, string) { return p.Branches, p.Pagination.Cursor })
}

// DefaultBranch returns the project's default branch, which new environment
// branches fork from.
func (c *Client) DefaultBranch(ctx context.Context, projectID string) (*Branch, error) {
	branches, err := c.listBranches(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range branches {
		if branches[i].Default {
			return &branches[i], nil
		}
	}
	return nil, kerrors.Validation("project %s has no default branch", projectID)
}

// FindBranchByName returns the branch called name, or nil when absent.
//
// Nil rather than an error: teardown asks this to decide whether there is
// anything to delete, and a branch that is already gone is success.
func (c *Client) FindBranchByName(ctx context.Context, projectID, name string) (*Branch, error) {
	branches, err := c.listBranches(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range branches {
		if branches[i].Name == name {
			return &branches[i], nil
		}
	}
	return nil, nil
}

// CreateBranch forks parentID into a new branch called name.
//
// The read_write endpoint is not optional: a branch created with no endpoints
// is storage only — no compute, no connection URI, and nothing able to query
// it. The branch would appear to exist and then be useless.
func (c *Client) CreateBranch(ctx context.Context, projectID, parentID, name string) (*Branch, error) {
	resp, err := do[struct {
		Branch Branch `json:"branch"`
	}](ctx, c, request{
		method: "POST",
		path:   "/projects/" + url.PathEscape(projectID) + "/branches",
		body: map[string]any{
			"branch":    map[string]string{"name": name, "parent_id": parentID},
			"endpoints": []map[string]string{{"type": "read_write"}},
		},
	})
	if err != nil {
		return nil, err
	}
	return &resp.Branch, nil
}

// DeleteBranch removes a branch.
func (c *Client) DeleteBranch(ctx context.Context, projectID, branchID string) error {
	_, err := do[map[string]any](ctx, c, request{
		method: "DELETE",
		path:   "/projects/" + url.PathEscape(projectID) + "/branches/" + url.PathEscape(branchID),
	})
	return err
}

// ConnectionOptions selects which connection URI to fetch.
type ConnectionOptions struct {
	BranchID string
	Database string
	Role     string
	// Pooled selects Neon's connection pooler. kraai asks for the direct
	// endpoint, since Hyperdrive does its own pooling in front of this.
	Pooled bool
}

// ConnectionURI fetches a connection string for a branch.
//
// The return value is a live credential. It must not be logged, embedded in
// an error, or passed as a command-line argument — parse it with
// internal/db.ParseConnectionURI and carry the parts, which is why nothing
// here returns it inside a wrapper that something might print.
func (c *Client) ConnectionURI(ctx context.Context, projectID string, opts ConnectionOptions) (string, error) {
	query := url.Values{}
	query.Set("branch_id", opts.BranchID)
	query.Set("database_name", opts.Database)
	query.Set("role_name", opts.Role)
	query.Set("pooled", strconv.FormatBool(opts.Pooled))

	resp, err := do[struct {
		URI string `json:"uri"`
	}](ctx, c, request{
		method: "GET",
		path:   "/projects/" + url.PathEscape(projectID) + "/connection_uri",
		query:  query,
	})
	if err != nil {
		return "", err
	}
	if resp.URI == "" {
		return "", kerrors.Validation("neon returned an empty connection URI for branch %s", opts.BranchID)
	}
	return resp.URI, nil
}
