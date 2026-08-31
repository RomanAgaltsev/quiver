package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"

	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx/echopb"
)

// collectionWithEnv builds a minimal collection whose dev environment points
// {{base}} at url, and makes it the working directory so ad-hoc history lands in
// a temp tree rather than the package directory.
func collectionWithEnv(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "dev.yaml"),
		[]byte("base: \""+url+"\"\ntok: t0k\n"), 0o644))
	return dir
}

// The README promises ad-hoc commands resolve --env/-V "in their arguments".
// Headers and query params are arguments too, and they were the ones skipped —
// which is exactly the case that matters, since the most valuable ad-hoc use of
// a variable is a token in a header.
func TestAdHocExpandsHeaderAndQueryValues(t *testing.T) {
	var seenAuth, seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenQuery = r.URL.Query().Get("b")
	}))
	defer srv.Close()

	dir := collectionWithEnv(t, srv.URL)
	_, _, code := run(t, "http", "GET", "{{base}}/x",
		"--collection", dir, "--env", "dev",
		"-H", "Authorization: Bearer {{tok}}",
		"-q", "b={{tok}}",
		"--quiet")
	require.Equal(t, 0, code)
	require.Equal(t, "Bearer t0k", seenAuth)
	require.Equal(t, "t0k", seenQuery)
}

// An ad-hoc call is exactly the kind of exploration history exists for, and the
// README says "every run records". It never did.
func TestAdHocIsRecordedInHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	dir := collectionWithEnv(t, srv.URL)
	_, _, code := run(t, "http", "GET", srv.URL, "--collection", dir, "--quiet")
	require.Equal(t, 0, code)

	out, _, code := run(t, "history", "list", "--collection", dir)
	require.Equal(t, 0, code)
	require.Contains(t, out, "GET "+srv.URL)
}

// An ad-hoc record has no source file, so replay must refuse it by design
// rather than dereferencing an empty path.
func TestReplayRefusesAdHocRecords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	dir := collectionWithEnv(t, srv.URL)
	_, _, code := run(t, "http", "GET", srv.URL, "--collection", dir, "--quiet")
	require.Equal(t, 0, code)

	out, _, _ := run(t, "history", "list", "--collection", dir)
	id := strings.Fields(out)[0]

	_, errOut, code := run(t, "history", "replay", id, "--collection", dir)
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "no source path")
}

// --query must not silently become an empty query string.
func TestAdHocGraphQLRequiresQuery(t *testing.T) {
	_, errOut, code := run(t, "graphql", "http://x/graphql", "--dry-run")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "--query is required")
}

func TestAdHocGraphQLSendsQueryAndVariables(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		_, _ = w.Write([]byte(`{"data":{"hero":{"name":"R2-D2"}}}`))
	}))
	defer srv.Close()

	dir := collectionWithEnv(t, srv.URL)
	_, _, code := run(t, "graphql", srv.URL, "-q", "{ hero { name } }",
		"--variables", `{"ep":"JEDI"}`, "--collection", dir, "--quiet")
	require.Equal(t, 0, code)
	require.Equal(t, "{ hero { name } }", body["query"])
	require.Equal(t, map[string]any{"ep": "JEDI"}, body["variables"])
}

// A GraphQL failure is an HTTP 200 carrying `errors`, and --check-status must
// treat it as a failure.
func TestAdHocGraphQLErrorsFailUnderCheckStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"boom"}]}`))
	}))
	defer srv.Close()

	dir := collectionWithEnv(t, srv.URL)
	_, _, code := run(t, "graphql", srv.URL, "-q", "{ x }", "--collection", dir, "--quiet")
	require.Equal(t, 0, code)

	_, _, code = run(t, "graphql", srv.URL, "-q", "{ x }", "--collection", dir, "--quiet", "--check-status")
	require.Equal(t, 1, code)
}

type echoServer struct{ echopb.UnimplementedEchoServer }

func (echoServer) Say(ctx context.Context, in *echopb.EchoRequest) (*echopb.EchoReply, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("authorization"); len(v) > 0 {
			_ = grpc.SetHeader(ctx, metadata.Pairs("x-seen-authorization", v[0]))
		}
	}
	return &echopb.EchoReply{Msg: "got:" + in.Msg}, nil
}

// startEchoOnPort serves the echo service on a real loopback port, because the
// ad-hoc command builds its own executor and cannot be handed a bufconn dialer.
func startEchoOnPort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	echopb.RegisterEchoServer(srv, echoServer{})
	reflection.Register(srv)

	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})
	return lis.Addr().String()
}

// Spec §6 documents `qv grpc <TARGET> <METHOD>`, matching grpcurl. The
// implementation took them the other way round, so typing the spec's own example
// produced an error about the method when the user had given the target.
func TestAdHocGRPCTakesTargetFirst(t *testing.T) {
	addr := startEchoOnPort(t)
	dir := collectionWithEnv(t, "http://unused")

	out, _, code := run(t, "grpc", addr, "echo.Echo/Say",
		"-d", `{"msg":"hi"}`, "--plaintext", "--collection", dir, "--output", "raw")
	require.Equal(t, 0, code)
	require.Contains(t, out, "got:hi")
}

// The reverse order is a plausible mistake, not a trap: only one argument can
// contain a "/", so it is unambiguous.
func TestAdHocGRPCToleratesReversedArguments(t *testing.T) {
	addr := startEchoOnPort(t)
	dir := collectionWithEnv(t, "http://unused")

	out, _, code := run(t, "grpc", "echo.Echo/Say", addr,
		"-d", `{"msg":"hi"}`, "--plaintext", "--collection", dir, "--output", "raw")
	require.Equal(t, 0, code)
	require.Contains(t, out, "got:hi")
}

// An auth profile is a first-class gRPC feature, so the ad-hoc form must express
// one too — `qv grpc` had no --bearer at all.
func TestAdHocGRPCBearerFlag(t *testing.T) {
	addr := startEchoOnPort(t)
	dir := collectionWithEnv(t, "http://unused")

	out, _, code := run(t, "grpc", addr, "echo.Echo/Say",
		"-d", `{"msg":"hi"}`, "--plaintext", "--bearer", "t0k",
		"--collection", dir, "--output", "json", "-v")
	require.Equal(t, 0, code)
	require.Contains(t, out, "Bearer t0k")
}

// A gRPC target that is simply down is a transport failure — exit 1 — the same
// way it is over HTTP.
func TestAdHocGRPCDeadTargetExitsOne(t *testing.T) {
	dir := collectionWithEnv(t, "http://unused")
	_, _, code := run(t, "grpc", "127.0.0.1:1", "echo.Echo/Say",
		"-d", "{}", "--plaintext", "--collection", dir, "--quiet")
	require.Equal(t, 1, code)
}
