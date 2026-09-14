// Package neon talks to the Neon management API for the database branches an
// ephemeral environment is given.
//
// Branching is what makes Neon the built-in: a branch is a copy-on-write fork
// of the parent's data with its own compute endpoint, so an environment gets
// a real database with real data in seconds and without a migration run —
// the thing D1 needs ApplyD1Migrations to approximate.
package neon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/evatt-labs/kraai/internal/kerrors"
)

// DefaultBaseURL is the Neon management API root.
const DefaultBaseURL = "https://console.neon.tech/api/v2"

// Response size ceilings, for the same reason the Cloudflare client has them:
// a remote response must not dictate this process's memory use, and a listing
// is far larger than an error.
const (
	maxErrorBody    = 64 << 10
	maxResponseBody = 32 << 20
)

// Client is a Neon management API client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient substitutes the transport.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// WithBaseURL points the client at a different API root, which is how tests
// aim it at an httptest server.
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimSuffix(u, "/") }
}

// New builds a client authenticating with apiKey.
func New(apiKey string, opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    DefaultBaseURL,
		apiKey:     apiKey,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// APIError is a non-2xx response from Neon.
type APIError struct {
	Status  int
	Path    string
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("neon API returned %d for %s", e.Status, e.Path)
	}
	return fmt.Sprintf("neon API returned %d for %s: %s (%s)", e.Status, e.Path, e.Message, e.Code)
}

// pagination is the cursor Neon returns on list endpoints. The cursor
// reflects the endpoint's sort field, and is passed back unchanged.
type pagination struct {
	Cursor string `json:"cursor"`
}

// request is one API call's inputs.
type request struct {
	method string
	path   string
	query  url.Values
	body   any
}

// do issues req and decodes the response into T.
//
// Neon does not wrap responses in a success envelope the way Cloudflare does:
// the body is the object itself, so T is the response shape directly.
func do[T any](ctx context.Context, c *Client, req request) (T, error) {
	var zero T

	var payload io.Reader
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "encoding request for %s", req.path)
		}
		payload = bytes.NewReader(encoded)
	}

	endpoint := c.baseURL + req.path
	if len(req.query) > 0 {
		endpoint += "?" + req.query.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.method, endpoint, payload)
	if err != nil {
		return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "building request for %s", req.path)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	if req.body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// Not wrapped: a transport error can carry the full URL, and the API
		// key travels in a header some proxies echo.
		return zero, kerrors.Validation("neon API request to %s failed", req.path)
	}
	defer func() { _ = resp.Body.Close() }()

	httpFailed := resp.StatusCode < 200 || resp.StatusCode >= 300
	limit := int64(maxResponseBody)
	if httpFailed {
		limit = maxErrorBody
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "reading response from %s", req.path)
	}
	if int64(len(raw)) > limit {
		if httpFailed {
			return zero, &APIError{Status: resp.StatusCode, Path: req.path}
		}
		return zero, kerrors.Validation("response from %s exceeded the %d-byte ceiling", req.path, limit)
	}

	if httpFailed {
		// Decoded into named fields rather than echoed whole. A response body
		// is not guaranteed to hold only what this end put in the request,
		// and one endpoint here returns a live connection string.
		var apiErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		return zero, &APIError{
			Status: resp.StatusCode, Path: req.path,
			Code: apiErr.Code, Message: apiErr.Message,
		}
	}

	var decoded T
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return zero, kerrors.Wrap(err, kerrors.CodeUnexpected, "decoding response from %s", req.path)
	}
	return decoded, nil
}

// maxListPages bounds a cursor walk, as a backstop against a cursor that
// never stops advancing.
const maxListPages = 100

// listCursor walks a cursor-paginated Neon collection.
//
// Neon's list endpoints are cursor-based and, critically, small by default:
// /projects returns ten unless told otherwise. Fetching one page and
// concluding a project does not exist is wrong for any account with more
// than ten of them, and the caller's response to "not found" here is to fail
// the run outright.
//
// extract pulls the items and the next cursor out of one page's response,
// since the collection key differs per endpoint.
func listCursor[R any, T any](
	ctx context.Context,
	c *Client,
	path string,
	query url.Values,
	extract func(R) ([]T, string),
) ([]T, error) {
	var all []T
	cursor := ""

	for page := 0; page < maxListPages; page++ {
		pageQuery := url.Values{}
		for k, v := range query {
			pageQuery[k] = v
		}
		if cursor != "" {
			pageQuery.Set("cursor", cursor)
		}

		resp, err := do[R](ctx, c, request{method: "GET", path: path, query: pageQuery})
		if err != nil {
			return nil, err
		}
		items, next := extract(resp)
		all = append(all, items...)

		// An empty page, an absent cursor, or a cursor that has not moved all
		// mean there is nothing further. The last of those is what stops a
		// spin if the API ever repeats itself.
		if len(items) == 0 || next == "" || next == cursor {
			return all, nil
		}
		cursor = next
	}
	return nil, kerrors.Validation("listing %s did not terminate within %d pages", path, maxListPages)
}
