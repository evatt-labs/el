package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"go.uber.org/mock/gomock"
)

func TestHTTPCapabilityName(t *testing.T) {
	c := NewHTTPCapability(nil)
	if c.Name() != CapabilityHTTPFetch {
		t.Fatalf("got %q, want %q", c.Name(), CapabilityHTTPFetch)
	}
}

func TestHTTPCapabilityInvokeSuccess(t *testing.T) {
	ctrl := gomock.NewController(t)
	doer := NewMockHTTPDoer(ctrl)
	doer.EXPECT().Do(gomock.Any()).DoAndReturn(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.String() != "https://example.invalid/x" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"X-Test": []string{"yes"}},
			Body:       io.NopCloser(bytes.NewReader([]byte("body-content"))),
		}, nil
	})

	c := NewHTTPCapability(doer)
	reqBody, _ := json.Marshal(map[string]any{"method": "GET", "url": "https://example.invalid/x"})

	out, err := c.Invoke(t.Context(), reqBody)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	var resp httpFetchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != "body-content" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestHTTPCapabilityInvokeRejectsBadRequest(t *testing.T) {
	c := NewHTTPCapability(nil)

	if _, err := c.Invoke(t.Context(), []byte("not json")); err == nil {
		t.Fatal("expected an error for undecodable input")
	}
	if _, err := c.Invoke(t.Context(), []byte(`{}`)); err == nil {
		t.Fatal("expected an error for a request missing method/url")
	}
}

func TestHTTPCapabilityInvokeSurfacesDoerError(t *testing.T) {
	ctrl := gomock.NewController(t)
	doer := NewMockHTTPDoer(ctrl)
	doer.EXPECT().Do(gomock.Any()).Return(nil, errors.New("network exploded"))

	c := NewHTTPCapability(doer)
	reqBody, _ := json.Marshal(map[string]any{"method": "GET", "url": "https://example.invalid/x"})

	if _, err := c.Invoke(t.Context(), reqBody); err == nil {
		t.Fatal("expected the doer's error to surface")
	}
}

func TestHTTPCapabilityInvokeRejectsOversizedBody(t *testing.T) {
	ctrl := gomock.NewController(t)
	doer := NewMockHTTPDoer(ctrl)
	oversized := bytes.Repeat([]byte("x"), MaxTransferBytes+1)
	doer.EXPECT().Do(gomock.Any()).Return(&http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader(oversized)),
	}, nil)

	c := NewHTTPCapability(doer)
	reqBody, _ := json.Marshal(map[string]any{"method": "GET", "url": "https://example.invalid/x"})

	if _, err := c.Invoke(t.Context(), reqBody); err == nil {
		t.Fatal("expected an error for a response body over MaxTransferBytes")
	}
}
