package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/env"
	"github.com/RomanAgaltsev/quiver/internal/history"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/httpx"
)

func TestRunFolderChainsCaptures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "secret"})
		case "/me":
			require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
			w.WriteHeader(200)
		}
	}))
	defer srv.Close()

	login := &request.Request{Name: "login", Protocol: request.ProtocolHTTP,
		HTTP:     &request.HTTPSpec{Method: "POST", URL: srv.URL + "/login"},
		Captures: []request.Capture{{Var: "tok", From: "body", Path: "token"}}}
	me := &request.Request{Name: "me", Protocol: request.ProtocolHTTP,
		HTTP:       &request.HTTPSpec{Method: "GET", URL: srv.URL + "/me", Headers: map[string]string{"Authorization": "Bearer {{tok}}"}},
		Assertions: []request.Assertion{{From: "status", Op: "eq", Value: "200"}}}

	reg := core.Registry{request.ProtocolHTTP: httpx.New()}
	rn := New(reg, nil, Options{})
	res := &env.Resolved{Vars: map[string]string{}}
	results := rn.RunFolder(context.Background(), []*request.Request{login, me}, res, nil)

	require.Len(t, results, 2)
	require.NoError(t, results[0].Err)
	require.Equal(t, "secret", results[0].Captured["tok"])
	require.NoError(t, results[1].Err)
	require.True(t, len(results[1].Assertions) == 1 && results[1].Assertions[0].Passed)
	require.Equal(t, 0, ExitCode(results))
}

// A request that got a response belongs in history even if a capture or
// assertion then errors. The previous revision returned early on capture error
// and never recorded — so the requests most worth investigating were the ones
// missing from the log.
func TestRunRequestRecordsHistoryEvenWhenCaptureFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	st, err := history.Open(t.TempDir(), nil)
	require.NoError(t, err)

	r := &request.Request{Name: "x", Protocol: request.ProtocolHTTP, Path: "x.yaml",
		HTTP:     &request.HTTPSpec{Method: "GET", URL: srv.URL},
		Captures: []request.Capture{{Var: "tok", From: "body", Path: "nope"}}}

	rn := New(core.Registry{request.ProtocolHTTP: httpx.New()}, st, Options{})
	res := rn.RunRequest(context.Background(), r, &env.Resolved{Vars: map[string]string{}}, nil)
	require.Error(t, res.Err) // the capture did fail

	recs, err := st.List()
	require.NoError(t, err)
	require.Len(t, recs, 1) // ...and the request was still recorded
	require.Equal(t, "x.yaml", recs[0].Path)
}

// A --var override is the user's debugging tool and must win over a capture
// of the same name.
func TestCapturesDoNotOverrideCLIVars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "captured"})
			return
		}
		require.Equal(t, "Bearer forced", r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	login := &request.Request{Name: "login", Protocol: request.ProtocolHTTP,
		HTTP:     &request.HTTPSpec{Method: "POST", URL: srv.URL + "/login"},
		Captures: []request.Capture{{Var: "tok", From: "body", Path: "token"}}}
	me := &request.Request{Name: "me", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL + "/me",
			Headers: map[string]string{"Authorization": "Bearer {{tok}}"}}}

	rn := New(core.Registry{request.ProtocolHTTP: httpx.New()}, nil,
		Options{Overrides: map[string]string{"tok": "forced"}})
	res := &env.Resolved{Vars: map[string]string{"tok": "forced"}}
	results := rn.RunFolder(context.Background(), []*request.Request{login, me}, res, nil)
	require.NoError(t, results[1].Err)
}

// With --check-status, a non-OK response fails the run even with no
// assertions declared.
func TestFailOnErrorMakesNon2xxFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	r := &request.Request{Name: "x", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL}}
	reg := core.Registry{request.ProtocolHTTP: httpx.New()}
	resolved := &env.Resolved{Vars: map[string]string{}}

	lenient := New(reg, nil, Options{}).RunFolder(context.Background(), []*request.Request{r}, resolved, nil)
	require.Equal(t, 0, ExitCode(lenient)) // documented default: assertions are the contract

	strict := New(reg, nil, Options{FailOnError: true}).RunFolder(context.Background(), []*request.Request{r}, resolved, nil)
	require.Equal(t, 1, ExitCode(strict))
}

// --dry-run must send nothing.
func TestDryRunSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()

	r := &request.Request{Name: "x", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL}}
	rn := New(core.Registry{request.ProtocolHTTP: httpx.New()}, nil, Options{DryRun: true})
	results := rn.RunFolder(context.Background(), []*request.Request{r}, &env.Resolved{Vars: map[string]string{}}, nil)

	require.Equal(t, 0, hits)
	require.NoError(t, results[0].Err)
	require.NotNil(t, results[0].Resolved)
	require.Nil(t, results[0].Response)
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
