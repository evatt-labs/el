package cloudflare

import (
	"context"
	"net/url"
	"strconv"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// R2Service manages R2 buckets.
//
// R2 is the odd one out: buckets are addressed by name directly, with no
// separate id, so there is no find-by-name step before a delete the way D1,
// KV and Queues need.
type R2Service struct{ c *Client }

// maxDeletePages bounds how many pages Delete will walk while emptying a
// bucket.
//
// A cursor that never advances would otherwise loop forever against a live
// API, and an ephemeral preview bucket holding more objects than this is far
// likelier to be a bug than a real workload. Hitting the bound reports
// rather than silently stopping.
const maxDeletePages = 1000

// r2PerPage is the list endpoint's documented maximum; its default is 20.
const r2PerPage = 1000

// R2Bucket is a bucket as the API reports it.
type R2Bucket struct {
	Name string `json:"name"`
}

// FindByName returns the bucket called name, or nil when absent.
//
// R2 has no get-by-name endpoint, so this lists with the name filter and
// matches exactly here — name_contains is a substring filter, not an equality
// test, so "env-a-api" would otherwise match "env-a-api-staging".
//
// Worth having even though Delete addresses buckets by name and needs no
// lookup: without it a plan cannot tell an existing bucket from a missing one
// and would propose creating one that is already there.
func (s *R2Service) FindByName(ctx context.Context, name string) (*R2Bucket, error) {
	query := url.Values{}
	query.Set("per_page", strconv.Itoa(r2PerPage))
	query.Set("name_contains", name)

	buckets, err := listAll[R2Bucket](ctx, s.c, s.c.accountPath("r2", "buckets"), r2PerPage, query)
	if err != nil {
		return nil, err
	}
	for i := range buckets {
		if buckets[i].Name == name {
			return &buckets[i], nil
		}
	}
	return nil, nil
}

// R2Object is one object in a bucket listing.
type R2Object struct {
	Key string `json:"key"`
}

// Create provisions a bucket and returns its name.
func (s *R2Service) Create(ctx context.Context, name string) (string, error) {
	bucket, err := do[struct {
		Name string `json:"name"`
	}](ctx, s.c, request{
		method: "POST",
		path:   s.c.accountPath("r2", "buckets"),
		body:   map[string]string{"name": name},
	})
	if err != nil {
		return "", err
	}
	return bucket.Name, nil
}

// Delete empties a bucket and then removes it.
//
// R2 refuses to delete a non-empty bucket. An ephemeral bucket only exists
// for its environment's lifetime, but the preview application may well have
// written objects to it, so this empties rather than assuming.
func (s *R2Service) Delete(ctx context.Context, name string) error {
	if err := s.empty(ctx, name); err != nil {
		return err
	}
	return doNoResult(ctx, s.c, request{
		method: "DELETE",
		path:   s.c.accountPath("r2", "buckets", name),
	})
}

// empty deletes every object in the bucket, following the listing cursor.
func (s *R2Service) empty(ctx context.Context, name string) error {
	cursor := ""
	for page := 0; ; page++ {
		if page >= maxDeletePages {
			return kerrors.Validation(
				"gave up emptying R2 bucket %q after %d pages — delete it from the dashboard",
				name, maxDeletePages)
		}

		query := url.Values{}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		listing, err := do[struct {
			Objects []R2Object `json:"objects"`
			Cursor  string     `json:"cursor"`
		}](ctx, s.c, request{
			method: "GET",
			path:   s.c.accountPath("r2", "buckets", name, "objects"),
			query:  query,
		})
		if err != nil {
			return err
		}

		for _, object := range listing.Objects {
			if err := doNoResult(ctx, s.c, request{
				method: "DELETE",
				path:   s.c.accountPath("r2", "buckets", name, "objects", object.Key),
			}); err != nil {
				return err
			}
		}

		// An empty cursor ends the walk. A cursor that repeats itself would
		// otherwise spin: the page bound above is the backstop, but stopping
		// on a non-advancing cursor catches it immediately.
		if listing.Cursor == "" || listing.Cursor == cursor {
			return nil
		}
		cursor = listing.Cursor
	}
}
