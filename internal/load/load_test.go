package load

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RomanAgaltsev/metronome"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/env"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/httpx"
)

func reg() core.Registry {
	return core.Registry{request.ProtocolHTTP: httpx.New()}
}

func resolved() *env.Resolved {
	return &env.Resolved{Vars: map[string]string{}}
}

// A load target may not declare captures: under load there is no coherent
// answer to "which response's token wins" across thousands of concurrent
// iterations. Rejecting is honest; a silent no-op would let the user believe a
// chain is happening when it is not.
func TestValidateTargetsRejectsCaptures(t *testing.T) {
	r := &request.Request{Name: "login", Protocol: request.ProtocolHTTP,
		HTTP:     &request.HTTPSpec{Method: "POST", URL: "http://x"},
		Captures: []request.Capture{{Var: "tok", From: "body", Path: "token"}}}

	err := ValidateTargets([]*request.Request{r})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tok")
	require.Contains(t, err.Error(), "--setup")
}

func TestValidateTargetsAcceptsCleanRequests(t *testing.T) {
	r := &request.Request{Name: "get", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "http://x"}}
	require.NoError(t, ValidateTargets([]*request.Request{r}))
}

func TestExecuteDrivesTheTarget(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	target := &request.Request{Name: "ping", Protocol: request.ProtocolHTTP, Path: "ping.yaml",
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL}}
	p, err := ResolveProfile(&request.LoadSpec{Rate: 500, Requests: 40}, Overrides{})
	require.NoError(t, err)

	run, err := Execute(context.Background(), Options{
		Registry: reg(), Targets: []*request.Request{target},
		Resolved: resolved(), Profile: p,
	})
	require.NoError(t, err)
	require.Equal(t, int64(40), run.Snapshot.Count) // exact-delivery contract
	require.Equal(t, int64(40), hits.Load())
	require.Equal(t, 0, run.Eval.ExitCode)
}

// The setup chain runs ONCE through the existing sequential runner, and its
// captures reach the load phase.
func TestExecuteRunsSetupChainOnce(t *testing.T) {
	var logins, authed atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			logins.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "s3cret"})
		case "/me":
			if r.Header.Get("Authorization") == "Bearer s3cret" {
				authed.Add(1)
			}
		}
	}))
	defer srv.Close()

	login := &request.Request{Name: "login", Protocol: request.ProtocolHTTP, Path: "login.yaml",
		HTTP:     &request.HTTPSpec{Method: "POST", URL: srv.URL + "/login"},
		Captures: []request.Capture{{Var: "tok", From: "body", Path: "token"}}}
	me := &request.Request{Name: "me", Protocol: request.ProtocolHTTP, Path: "me.yaml",
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL + "/me",
			Headers: map[string]string{"Authorization": "Bearer {{tok}}"}}}

	p, err := ResolveProfile(&request.LoadSpec{Rate: 500, Requests: 25}, Overrides{})
	require.NoError(t, err)

	run, err := Execute(context.Background(), Options{
		Registry: reg(), Targets: []*request.Request{me}, Setup: []*request.Request{login},
		Resolved: resolved(), Profile: p,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), logins.Load(), "setup must run exactly once")
	require.Equal(t, int64(25), authed.Load(), "every load request carried the captured token")
	require.Equal(t, int64(25), run.Snapshot.Count)
}

// A setup failure aborts before a single load request is sent.
func TestExecuteAbortsWhenSetupFails(t *testing.T) {
	var loadHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.WriteHeader(500)
			return
		}
		loadHits.Add(1)
	}))
	defer srv.Close()

	statusValue := "200"

	login := &request.Request{Name: "login", Protocol: request.ProtocolHTTP, Path: "login.yaml",
		HTTP:       &request.HTTPSpec{Method: "POST", URL: srv.URL + "/login"},
		Assertions: []request.Assertion{{From: "status", Op: "eq", Value: &statusValue}}}
	target := &request.Request{Name: "x", Protocol: request.ProtocolHTTP, Path: "x.yaml",
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL + "/x"}}

	p, _ := ResolveProfile(&request.LoadSpec{Rate: 100, Requests: 10}, Overrides{})
	_, err := Execute(context.Background(), Options{
		Registry: reg(), Targets: []*request.Request{target}, Setup: []*request.Request{login},
		Resolved: resolved(), Profile: p,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "setup")
	require.Zero(t, loadHits.Load())
}

// A folder target becomes a weighted Mix; every request appears.
func TestExecuteFolderTargetUsesWeightedMix(t *testing.T) {
	var a, b atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/a" {
			a.Add(1)
		} else {
			b.Add(1)
		}
	}))
	defer srv.Close()

	mk := func(name, path string, weight int) *request.Request {
		return &request.Request{Name: name, Protocol: request.ProtocolHTTP, Path: name + ".yaml",
			HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL + path},
			Load: &request.LoadSpec{Weight: weight}}
	}
	p, _ := ResolveProfile(&request.LoadSpec{Rate: 2000, Requests: 400}, Overrides{})
	run, err := Execute(context.Background(), Options{
		Registry: reg(),
		Targets:  []*request.Request{mk("a", "/a", 3), mk("b", "/b", 1)},
		Resolved: resolved(), Profile: p,
	})
	require.NoError(t, err)
	require.Equal(t, int64(400), run.Snapshot.Count)
	require.Positive(t, a.Load())
	require.Positive(t, b.Load())
	require.Greater(t, a.Load(), b.Load(), "weight 3 must be picked more often than weight 1")
}

// Pacing is asserted under a ManualClock, so no test sleeps for real.
func TestExecuteUsesInjectedClock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	target := &request.Request{Name: "t", Protocol: request.ProtocolHTTP, Path: "t.yaml",
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL}}
	p, _ := ResolveProfile(&request.LoadSpec{Rate: 10, Requests: 3}, Overrides{})

	clock := metronome.NewManualClock(time.Now())
	done := make(chan Run, 1)
	go func() {
		run, err := Execute(context.Background(), Options{
			Registry: reg(), Targets: []*request.Request{target},
			Resolved: resolved(), Profile: p, Clock: clock,
		})
		require.NoError(t, err)
		done <- run
	}()

	// 10 rps -> one unit every 100ms. Advance until the run completes rather
	// than a fixed three times: the driver's rate updater also sleeps on this
	// clock, so BlockUntilSleepers(1) can be satisfied by the updater while the
	// dispatcher has yet to reserve its next slot, leaving the dispatcher's
	// sleeper beyond the last advance with nothing left to wake it. Extra
	// advances are harmless — deadlines are absolute and the run ends on
	// MaxRequests, not on elapsed time.
	for range 100 {
		select {
		case run := <-done:
			require.Equal(t, int64(3), run.Snapshot.Count)
			return
		default:
		}
		clock.Advance(100 * time.Millisecond)
		time.Sleep(time.Millisecond) // yield so the dispatcher can reserve its next slot
	}
	t.Fatal("Execute did not finish under the manual clock")
}

// Cancelling mid-run must not leak: metronome's contract is that abandoning a
// live result channel leaks its goroutines for the lifetime of the process.
func TestExecuteDrainsOnCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
	}))
	defer srv.Close()

	target := &request.Request{Name: "t", Protocol: request.ProtocolHTTP, Path: "t.yaml",
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL}}
	p, _ := ResolveProfile(&request.LoadSpec{Rate: 200, Duration: secs(time.Minute)}, Overrides{})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	run, err := Execute(ctx, Options{
		Registry: reg(), Targets: []*request.Request{target}, Resolved: resolved(), Profile: p,
	})
	require.NoError(t, err) // cancellation ends the run, it is not an error
	require.Positive(t, run.Snapshot.Count)
	// goleak in TestMain proves nothing was left behind.
}

func TestExecuteRejectsUnresolvableTarget(t *testing.T) {
	target := &request.Request{Name: "t", Protocol: request.ProtocolHTTP, Path: "t.yaml",
		HTTP: &request.HTTPSpec{Method: "GET", URL: "{{missing}}/x"}}
	p, _ := ResolveProfile(&request.LoadSpec{Rate: 10, Requests: 1}, Overrides{})

	_, err := Execute(context.Background(), Options{
		Registry: reg(), Targets: []*request.Request{target}, Resolved: resolved(), Profile: p,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing")
}
