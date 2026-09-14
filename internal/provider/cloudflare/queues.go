package cloudflare

import "context"

// QueuesService manages Queues.
type QueuesService struct{ c *Client }

// Queue is a queue as the API reports it.
type Queue struct {
	ID   string `json:"queue_id"`
	Name string `json:"queue_name"`
}

// Create provisions a queue and returns its id.
func (s *QueuesService) Create(ctx context.Context, name string) (string, error) {
	q, err := do[Queue](ctx, s.c, request{
		method: "POST",
		path:   s.c.accountPath("queues"),
		body:   map[string]string{"queue_name": name},
	})
	if err != nil {
		return "", err
	}
	return q.ID, nil
}

// FindByName returns the queue called name, or nil when absent.
//
// Unpaged deliberately. Unlike the KV and Hyperdrive list endpoints, this one
// declares no page or per_page parameter at all in Cloudflare's OpenAPI spec,
// so it returns the account's queues in one response and adding paging
// parameters would be guessing at an API that does not offer them.
func (s *QueuesService) FindByName(ctx context.Context, name string) (*Queue, error) {
	queues, err := do[[]Queue](ctx, s.c, request{
		method: "GET",
		path:   s.c.accountPath("queues"),
	})
	if err != nil {
		return nil, err
	}
	for i := range queues {
		if queues[i].Name == name {
			return &queues[i], nil
		}
	}
	return nil, nil
}

// Delete removes a queue by id.
func (s *QueuesService) Delete(ctx context.Context, queueID string) error {
	return doNoResult(ctx, s.c, request{
		method: "DELETE",
		path:   s.c.accountPath("queues", queueID),
	})
}
