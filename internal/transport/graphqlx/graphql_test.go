package graphqlx

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/httpx"
)

func TestGraphQLExecute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &payload))
		require.Contains(t, payload.Query, "users")
		require.Equal(t, float64(7), payload.Variables["id"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"users":[{"id":7}]}}`))
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "q", Protocol: request.ProtocolGraphQL,
		GraphQL: &request.GraphQLSpec{URL: srv.URL, Query: "query($id:Int){users(id:$id){id}}", Variables: `{"id":7}`}}
	resp, err := New(httpx.New()).Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, 200, resp.Status)
	require.True(t, resp.OK)
	require.Contains(t, string(resp.Body), `"users"`)
}

// A GraphQL failure is an HTTP 200 carrying an `errors` array. The previous
// revision never looked, so `qv run` rendered the error payload and exited 0 —
// a silent false negative in one of the three headline protocols.
func TestGraphQLExecuteDetectsApplicationErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"boom"}]}`))
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "q", Protocol: request.ProtocolGraphQL,
		GraphQL: &request.GraphQLSpec{URL: srv.URL, Query: "{ users { id } }"}}
	resp, err := New(httpx.New()).Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, 200, resp.Status) // still a 200 at the HTTP layer
	require.False(t, resp.OK)          // but not a success
	require.Equal(t, "graphql error", resp.StatusText)
}

// An empty errors array is not a failure.
func TestGraphQLExecuteEmptyErrorsArrayIsOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"x":1},"errors":[]}`))
	}))
	defer srv.Close()

	rr := core.ResolvedRequest{Name: "q", Protocol: request.ProtocolGraphQL,
		GraphQL: &request.GraphQLSpec{URL: srv.URL, Query: "{ x }"}}
	resp, err := New(httpx.New()).Execute(context.Background(), rr)
	require.NoError(t, err)
	require.True(t, resp.OK)
}

// The transport is injected, so it can be stubbed.
func TestGraphQLUsesInjectedExecutor(t *testing.T) {
	var got core.ResolvedRequest
	stub := core.ExecutorFunc(func(_ context.Context, rr core.ResolvedRequest) (*core.Response, error) {
		got = rr
		return &core.Response{Status: 200, OK: true, Body: []byte(`{"data":{}}`)}, nil
	})
	rr := core.ResolvedRequest{Name: "q", Protocol: request.ProtocolGraphQL,
		GraphQL: &request.GraphQLSpec{URL: "http://x/graphql", Query: "{ a }"}}
	_, err := New(stub).Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, "POST", got.HTTP.Method)
	require.Equal(t, "application/json", got.HTTP.Headers["Content-Type"])
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
