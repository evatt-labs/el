package reachability

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastOpts(client *http.Client) Options {
	return Options{Attempts: 10, Delay: time.Millisecond, ConsecutiveSuccesses: 3, Client: client}
}

// TestWaitRequiresAStreak is the anycast property: one success only proves the
// single point of presence that request reached is ready. A server that
// alternates good and edge-error responses must never satisfy the wait.
func TestWaitRequiresAStreak(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1)%2 == 0 {
			_, _ = w.Write([]byte("error code: 1042"))
			return
		}
		_, _ = w.Write([]byte("hello from the worker"))
	}))
	defer srv.Close()

	if Wait(t.Context(), srv.URL, fastOpts(srv.Client())) {
		t.Fatal("an alternating server satisfied the streak — one good probe was treated as ready")
	}
}

func TestWaitSucceedsOnceStable(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) <= 2 {
			_, _ = w.Write([]byte("error code: 1042"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	if !Wait(t.Context(), srv.URL, fastOpts(srv.Client())) {
		t.Fatal("a server that settled was never reported reachable")
	}
}

// An application's own error response is not the edge's. Only Cloudflare's
// fallback page means "not propagated yet"; a 404 from a real Worker means the
// Worker is up and answering.
func TestWaitTreatsApplicationErrorsAsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	if !Wait(t.Context(), srv.URL, fastOpts(srv.Client())) {
		t.Fatal("a real application 404 was mistaken for the edge-error page")
	}
}

func TestWaitGivesUpWithoutFailing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("error code: 1042"))
	}))
	defer srv.Close()

	// Best effort: exhausting the window reports false rather than erroring,
	// because the environment is genuinely provisioned by this point.
	if Wait(t.Context(), srv.URL, fastOpts(srv.Client())) {
		t.Fatal("a permanently unpropagated host was reported reachable")
	}
}

func TestWaitHandlesUnreachableHost(t *testing.T) {
	// A connection failure is the same "not ready" bucket as an edge-error
	// page: DNS may simply not resolve yet.
	if Wait(t.Context(), "http://127.0.0.1:1/nothing-listening", Options{
		Attempts: 2, Delay: time.Millisecond, ConsecutiveSuccesses: 1,
	}) {
		t.Fatal("a refused connection was reported reachable")
	}
}
