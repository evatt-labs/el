package cloudflare

import "context"

// KVService manages Workers KV namespaces.
type KVService struct{ c *Client }

// KVNamespace is a KV namespace as the API reports it. The human-readable
// name is "title" here, unlike every other resource type.
type KVNamespace struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Create provisions a namespace and returns its id.
func (s *KVService) Create(ctx context.Context, title string) (string, error) {
	ns, err := do[KVNamespace](ctx, s.c, request{
		method: "POST",
		path:   s.c.accountPath("storage", "kv", "namespaces"),
		body:   map[string]string{"title": title},
	})
	if err != nil {
		return "", err
	}
	return ns.ID, nil
}

// FindByTitle returns the namespace with that title, or nil when absent.
func (s *KVService) FindByTitle(ctx context.Context, title string) (*KVNamespace, error) {
	namespaces, err := do[[]KVNamespace](ctx, s.c, request{
		method: "GET",
		path:   s.c.accountPath("storage", "kv", "namespaces"),
	})
	if err != nil {
		return nil, err
	}
	for i := range namespaces {
		if namespaces[i].Title == title {
			return &namespaces[i], nil
		}
	}
	return nil, nil
}

// Delete removes a namespace by id.
func (s *KVService) Delete(ctx context.Context, namespaceID string) error {
	return doNoResult(ctx, s.c, request{
		method: "DELETE",
		path:   s.c.accountPath("storage", "kv", "namespaces", namespaceID),
	})
}
