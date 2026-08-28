package httpx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func TestHTTPExecuteGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "2", r.URL.Query().Get("page"))
		require.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{
		Name: "u", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL, Query: map[string]string{"page": "2"}},
		Auth: &request.AuthProfile{Type: "bearer", Token: "tok"},
	}
	resp, err := New().Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, 200, resp.Status)
	require.Equal(t, "application/json", resp.HeaderGet("Content-Type"))
	require.Contains(t, string(resp.Body), `"ok":true`)
}

func TestHTTPExecutePOSTBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		require.JSONEq(t, `{"x":1}`, string(b))
		w.WriteHeader(201)
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "c", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "POST", URL: srv.URL, Body: `{"x":1}`,
			Headers: map[string]string{"Content-Type": "application/json"}}}
	resp, err := New().Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, 201, resp.Status)
	require.True(t, resp.OK)
}

// OK is protocol-normalized success, so runner/--check-status never has to
// know that "< 400" is the HTTP rule.
func TestHTTPExecuteSetsOKFalseOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL}}
	resp, err := New().Execute(context.Background(), rr)
	require.NoError(t, err) // a 503 is a response, not a transport failure
	require.Equal(t, 503, resp.Status)
	require.False(t, resp.OK)
}

// A per-request timeout must actually cancel the request.
func TestHTTPExecuteHonoursPerRequestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "slow", Protocol: request.ProtocolHTTP,
		HTTP:    &request.HTTPSpec{Method: "GET", URL: srv.URL},
		Timeout: 20 * time.Millisecond}
	_, err := New().Execute(context.Background(), rr)
	require.Error(t, err)
}

// Query values are merged into any query string already on the URL.
func TestHTTPExecuteMergesQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "keep", r.URL.Query().Get("existing"))
		require.Equal(t, "https://cb/x", r.URL.Query().Get("next"))
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "q", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: srv.URL + "?existing=keep",
			Query: map[string]string{"next": "https://cb/x"}}}
	_, err := New().Execute(context.Background(), rr)
	require.NoError(t, err)
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
