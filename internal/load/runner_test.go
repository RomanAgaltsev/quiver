package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/RomanAgaltsev/metronome"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/httpx"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func httpTarget(t *testing.T, h http.HandlerFunc) (core.ResolvedRequest, func()) {
	t.Helper()
	srv := httptest.NewServer(h)
	rr := core.ResolvedRequest{
		Name: "probe", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL},
	}
	return rr, srv.Close
}

func TestRunnerMapsSuccessfulResponse(t *testing.T) {
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer done()

	res := newExecutorRunner(httpx.New(), rr, "probe", nil, nil).Do(context.Background())
	require.NoError(t, res.Err)
	require.True(t, res.Success())
	require.Equal(t, "200 OK", res.Code)
	require.Equal(t, int64(11), res.Bytes)
	require.Positive(t, res.Latency)
	require.False(t, res.Start.IsZero())
	require.Equal(t, "probe", res.Labels["request"])
}

// A non-OK response is a failed Result, not a transport error: the run
// continues and the failure lands in ErrorRate.
func TestRunnerMarksNonOKAsFailure(t *testing.T) {
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	})
	defer done()

	res := newExecutorRunner(httpx.New(), rr, "probe", nil, nil).Do(context.Background())
	require.Error(t, res.Err)
	require.False(t, res.Success())
	require.Contains(t, res.Err.Error(), "503")
}

func TestRunnerReportsTransportError(t *testing.T) {
	rr := core.ResolvedRequest{
		Name: "dead", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "http://127.0.0.1:1/nope"},
	}
	res := newExecutorRunner(httpx.New(), rr, "dead", nil, nil).Do(context.Background())
	require.Error(t, res.Err)
	require.Positive(t, res.Latency) // measured even on failure
}

func TestRunnerRunsAssertions(t *testing.T) {
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"ada"}`))
	})
	defer done()

	passValue := "ada"
	failValue := "grace"

	pass := []request.Assertion{{Name: "has name", From: "body", Path: "name", Op: "eq", Value: &passValue}}
	res := newExecutorRunner(httpx.New(), rr, "probe", pass, nil).Do(context.Background())
	require.NoError(t, res.Err)

	fail := []request.Assertion{{Name: "wrong", From: "body", Path: "name", Op: "eq", Value: &failValue}}
	res = newExecutorRunner(httpx.New(), rr, "probe", fail, nil).Do(context.Background())
	require.Error(t, res.Err)
	require.Contains(t, res.Err.Error(), "wrong")
}

// Latency comes from the executor's own measurement, not a wall-clock span
// around Execute, so quiver's dispatch overhead stays out of the target's
// percentiles.
func TestRunnerUsesExecutorMeasuredLatency(t *testing.T) {
	want := 1234 * time.Millisecond
	stub := core.ExecutorFunc(func(context.Context, core.ResolvedRequest) (*core.Response, error) {
		return &core.Response{Status: 200, StatusText: "200 OK", OK: true, Duration: want}, nil
	})
	res := newExecutorRunner(stub, core.ResolvedRequest{}, "probe", nil, nil).Do(context.Background())
	require.Equal(t, want, res.Latency)
}

// The Driver shares ONE ResolvedRequest across N goroutines. That it is
// read-only is a contract, not an accident of today's executors.
func TestRunnerIsSafeForConcurrentDo(t *testing.T) {
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer done()

	r := newExecutorRunner(httpx.New(), rr, "probe", nil, nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := r.Do(context.Background())
			require.NoError(t, res.Err)
		}()
	}
	wg.Wait()
}

// A panicking executor must not abort the run: metronome recovers it into a
// PanicError Result. This test proves quiver does not swallow it first.
func TestRunnerPanicPropagatesToDriver(t *testing.T) {
	stub := core.ExecutorFunc(func(context.Context, core.ResolvedRequest) (*core.Response, error) {
		panic("boom")
	})
	r := newExecutorRunner(stub, core.ResolvedRequest{}, "probe", nil, nil)
	require.Panics(t, func() { _ = r.Do(context.Background()) })

	var pe *metronome.PanicError
	d := metronome.Driver{Runner: r, Rate: metronome.Constant(1000), Workers: 1, MaxRequests: 1}
	for res := range d.Run(context.Background()) {
		require.ErrorAs(t, res.Err, &pe)
	}
}

// ── 2026-09-03 review, M2 ────────────────────────────────────────────────────
//
// A non-OK response used to short-circuit the assertions entirely, so the same
// request file meant different things under `qv run` and `qv load`: run only
// fails on non-OK with --check-status and otherwise lets assertions decide,
// while load always failed and never evaluated them. An error-path load test —
// assert status eq 404 — reported a 100% error rate.

func TestAssertionsDecideOnANonOKResponse(t *testing.T) {
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	})
	defer done()

	expect404 := []request.Assertion{
		{Name: "is 404", From: "status", Op: "eq", Value: request.Val("404")},
	}

	res := newExecutorRunner(httpx.New(), rr, "probe", expect404, nil).Do(context.Background())
	require.NoError(t, res.Err, "the declared assertion passed, so the iteration succeeded")
	require.True(t, res.Success())
	require.Equal(t, "404 Not Found", res.Code)
}

func TestFailingAssertionOnANonOKResponseIsStillAnError(t *testing.T) {
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	})
	defer done()

	expect404 := []request.Assertion{
		{Name: "is 404", From: "status", Op: "eq", Value: request.Val("404")},
	}

	res := newExecutorRunner(httpx.New(), rr, "probe", expect404, nil).Do(context.Background())
	require.Error(t, res.Err)
	require.Contains(t, res.Err.Error(), "is 404")
}

func TestNonOKIsStillAnErrorWhenNoAssertionsAreDeclared(t *testing.T) {
	// The fallback, and the common case: with nothing declared, a non-OK
	// response is the only success signal there is.
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	})
	defer done()

	res := newExecutorRunner(httpx.New(), rr, "probe", nil, nil).Do(context.Background())
	require.Error(t, res.Err)
	require.Contains(t, res.Err.Error(), "503")
}

// ── 2026-09-03 review, L1 ────────────────────────────────────────────────────

func TestRunnerStampsStartFromTheInjectedClock(t *testing.T) {
	// Result.Start feeds metronome's RPS inference. Reading the wall clock while
	// the Driver runs on a ManualClock makes those figures describe real elapsed
	// time, which in a fast test is nearly zero.
	rr, done := httpTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	defer done()

	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := metronome.NewManualClock(epoch)

	res := newExecutorRunner(httpx.New(), rr, "probe", nil, clk).Do(context.Background())
	require.Equal(t, epoch, res.Start, "Start must come from the injected clock")
}
