package httpx

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// countingServer wraps an httptest.Server and counts distinct TCP
// connections it accepts, via Server.Config.ConnState — the same mechanism
// the task brief asks for, and the only way to observe "how many
// connections were actually opened" from outside the client: the client's
// own view (responses received) cannot distinguish a reused connection from
// a fresh one, but the server sees every new accept as a transition into
// StateNew exactly once per connection.
type countingServer struct {
	*httptest.Server
	newConns int64
}

func newCountingServer(handler http.HandlerFunc) *countingServer {
	cs := &countingServer{Server: httptest.NewUnstartedServer(handler)}
	cs.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt64(&cs.newConns, 1)
		}
	}
	cs.Start()
	return cs
}

func (cs *countingServer) connections() int64 {
	return atomic.LoadInt64(&cs.newConns)
}

// fire runs workers concurrent goroutines, each issuing perWorker sequential
// GET requests against url through client, and waits for all of them to
// complete, failing the test on any request error.
//
// Sequential-per-worker, not one goroutine per request: net/http's
// MaxIdleConnsPerHost bounds *idle* connections, not in-flight ones, so a
// single burst of N simultaneous requests against an empty idle pool opens
// up to N connections regardless of pool tuning — nothing has gone idle yet
// for a later request to reuse. That shape does not exist in production:
// internal/apply and internal/plan bound concurrent in-flight work with
// errgroup.SetLimit(defaultConcurrency), so a worker finishes one call
// before starting its next. This mirrors that — workers stays at or below
// the concurrency bound the pool is actually sized for, and each worker's
// own connection goes idle and gets reused by its next call.
func fire(t *testing.T, client *http.Client, url string, workers, perWorker int) {
	t.Helper()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				resp, err := client.Get(url)
				if err != nil {
					t.Errorf("GET: %v", err)
					return
				}
				_ = resp.Body.Close()
			}
		}()
	}
	wg.Wait()
}

// TestSharedTransportReusesConnections is the connection-reuse evidence the
// task brief asks for. It issues many more concurrent requests than
// maxIdleConnsPerHost against one host, in two waves, through a client built
// by NewClient, and asserts the server saw at most maxIdleConnsPerHost
// distinct connections across both waves — proof the pool is bounded and
// actually reused rather than growing with the request count, which is what
// happens against Go's untuned default (DefaultMaxIdleConnsPerHost == 2, see
// the contrast test below).
func TestSharedTransportReusesConnections(t *testing.T) {
	// workers matches internal/apply's and internal/plan's own
	// defaultConcurrency — the real bound a phase runs its calls under —
	// not an arbitrary number.
	const workers = 10
	const perWorker = 20
	const totalRequests = workers * perWorker

	srv := newCountingServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	client := NewClient(5*time.Second, nil, nil)
	fire(t, client, srv.URL, workers, perWorker)

	got := srv.connections()
	if got > maxIdleConnsPerHost {
		t.Fatalf("shared transport opened %d connections for %d requests across %d workers, want at most %d (maxIdleConnsPerHost)",
			got, totalRequests, workers, maxIdleConnsPerHost)
	}
	if got >= int64(totalRequests) {
		t.Fatalf("shared transport opened %d connections for %d requests, want far fewer — no reuse happened", got, totalRequests)
	}
	t.Logf("tuned transport: %d requests across %d workers -> %d distinct connections (cap %d)",
		totalRequests, workers, got, maxIdleConnsPerHost)
}

// TestUntunedTransportChurnsMoreThanTuned is the contrast: the identical
// harness driven through both transports, asserting the untuned one opens
// meaningfully more connections than the tuned one.
//
// Deliberately a RELATIVE comparison rather than an absolute threshold.
// Churn is a consequence of how much requests actually overlap, which
// depends on machine speed — an earlier version asserted the untuned
// transport exceeded a fixed count and failed on CI, because a fast runner
// served the requests nearly serially and two idle connections sufficed.
// That was not the untuned transport behaving well; it was the test failing
// to create the condition it claimed to measure, then reporting the absence
// of that condition as a defect.
//
// Measuring both in one run cancels machine speed out: whatever overlap the
// host produces applies equally to both halves, so the only variable left is
// the pool size, which is the thing under test.
func TestUntunedTransportChurnsMoreThanTuned(t *testing.T) {
	const workers = 10
	const perWorker = 20
	const totalRequests = workers * perWorker

	// The handler holds each request briefly so workers are genuinely in
	// flight together. Without overlap there is nothing for a connection
	// pool to do, and both transports would look identical.
	const handlerDelay = 3 * time.Millisecond
	handler := func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(handlerDelay)
		w.WriteHeader(http.StatusOK)
	}

	measure := func(client *http.Client) int64 {
		srv := newCountingServer(handler)
		defer srv.Close()
		fire(t, client, srv.URL, workers, perWorker)
		return srv.connections()
	}

	// Untuned: a fresh, unshared transport at Go's defaults — exactly the
	// &http.Client{Timeout: ...} shape reachability, neon and cloudflare
	// each built independently before this package existed.
	untuned := measure(&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}})
	// Tuned: the real production constructor, not a hand-rebuilt equivalent,
	// so this measures what callers actually get.
	tuned := measure(NewClient(5*time.Second, nil, nil))

	t.Logf("%d requests across %d workers -> untuned %d connections, tuned %d (MaxIdleConnsPerHost=%d)",
		totalRequests, workers, untuned, tuned, maxIdleConnsPerHost)

	if tuned > maxIdleConnsPerHost {
		t.Errorf("tuned transport opened %d connections, want at most the pool size %d",
			tuned, maxIdleConnsPerHost)
	}
	if untuned <= tuned {
		t.Errorf("untuned transport opened %d connections and tuned opened %d: "+
			"want the untuned default to churn strictly more, or this harness is not "+
			"creating enough overlap to measure pooling at all", untuned, tuned)
	}
}
func TestTwoCallersShareOnePool(t *testing.T) {
	const workersEach = 5
	const perWorker = 20
	const totalRequests = 2 * workersEach * perWorker

	srv := newCountingServer(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer srv.Close()

	// 60s and 3s: neon/cloudflare's management-API timeout and
	// reachability's probe timeout, respectively — genuinely different
	// per-caller policy, built through the same NewClient.
	managementAPIClient := NewClient(60*time.Second, nil, nil)
	probeClient := NewClient(3*time.Second, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); fire(t, managementAPIClient, srv.URL, workersEach, perWorker) }()
	go func() { defer wg.Done(); fire(t, probeClient, srv.URL, workersEach, perWorker) }()
	wg.Wait()

	got := srv.connections()
	if got > maxIdleConnsPerHost {
		t.Fatalf("two differently-timed-out clients opened %d connections for %d total requests, want at most %d — pool is not actually shared",
			got, totalRequests, maxIdleConnsPerHost)
	}
	t.Logf("two clients, one pool: %d total requests -> %d distinct connections", totalRequests, got)
}

// TestPerCallerTimeoutHonoured proves the other half of the split design:
// sharing Transport does not collapse each caller's own Timeout into one
// shared value. A client with a timeout shorter than the handler's delay
// must fail; a client with a timeout longer than the same delay, built from
// the same Transport, must succeed.
func TestPerCallerTimeoutHonoured(t *testing.T) {
	const handlerDelay = 150 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(handlerDelay)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	shortTimeout := NewClient(20*time.Millisecond, nil, nil)
	resp, err := shortTimeout.Get(srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("client with a timeout shorter than the handler delay succeeded, want a timeout error")
	}

	longTimeout := NewClient(time.Second, nil, nil)
	resp, err = longTimeout.Get(srv.URL)
	if err != nil {
		t.Fatalf("client with a timeout longer than the handler delay failed: %v", err)
	}
	_ = resp.Body.Close()
}

// TestNewClientTracesRequests confirms the D17 half of this package: a
// request issued through a NewClient-built client produces a span, using an
// in-memory exporter the same way internal/resource/otel_test.go's own tests
// avoid needing a real collector.
func TestNewClientTracesRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	client := NewClient(5*time.Second, tp, nil)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("got %d spans, want exactly 1 for one request", len(ended))
	}
}
