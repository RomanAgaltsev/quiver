package grpcx

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx/echopb"
)

type echoServer struct{ echopb.UnimplementedEchoServer }

func (echoServer) Say(ctx context.Context, in *echopb.EchoRequest) (*echopb.EchoReply, error) {
	if in.Msg == "boom" {
		return nil, status.Error(codes.NotFound, "no such echo")
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", "abc"))
	_ = grpc.SetTrailer(ctx, metadata.Pairs("x-elapsed", "1ms"))
	// Count is deliberately left at its zero value.
	return &echopb.EchoReply{Msg: "got:" + in.Msg}, nil
}

// startEcho brings up a bufconn echo server with reflection enabled and returns a
// dialer plus a cleanup that waits for Serve to return (so goleak stays quiet).
func startEcho(t *testing.T) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	echopb.RegisterEchoServer(srv, echoServer{})
	reflection.Register(srv)

	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		<-done          // the previous revision never waited, which goleak flags
		_ = lis.Close() // ...and never closed the listener
	})
	return func(context.Context, string) (net.Conn, error) { return lis.Dial() }
}

func TestGRPCUnaryReflection(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say", Message: `{"msg":"hi"}`, Plaintext: true}}
	resp, err := exec.Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, 0, resp.Status) // codes.OK
	require.Equal(t, "OK", resp.StatusText)
	require.True(t, resp.OK) // Status==0 is ambiguous; OK is not
	require.Contains(t, string(resp.Body), `"got:hi"`)
}

// A zero-valued reply field must appear in the JSON, or captures and
// assertions on it fail with a baffling "path not found".
func TestGRPCEmitsDefaultValuedFields(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say", Message: `{"msg":"hi"}`, Plaintext: true}}
	resp, err := exec.Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Contains(t, string(resp.Body), `"count"`)
}

// Leading and trailing metadata must reach Response.Headers.
func TestGRPCCapturesMetadata(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say", Message: `{"msg":"hi"}`, Plaintext: true}}
	resp, err := exec.Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, "abc", resp.HeaderGet("X-Request-Id")) // leading
	require.Equal(t, "1ms", resp.HeaderGet("X-Elapsed"))    // trailing
}

// A non-OK gRPC status is an inspectable response, not a transport error.
func TestGRPCErrorStatusIsAResponse(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say", Message: `{"msg":"boom"}`, Plaintext: true}}
	resp, err := exec.Execute(context.Background(), rr)
	require.NoError(t, err)
	require.Equal(t, int(codes.NotFound), resp.Status)
	require.Equal(t, "NotFound", resp.StatusText)
	require.False(t, resp.OK)
}

// A second call to the same target must reuse the cached connection and
// descriptors rather than re-dialling and re-reflecting.
func TestGRPCReusesConnectionPerTarget(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say", Message: `{"msg":"hi"}`, Plaintext: true}}
	for range 3 {
		_, err := exec.Execute(context.Background(), rr)
		require.NoError(t, err)
	}
	require.Equal(t, 1, exec.(*executor).connCount())
}

func TestSplitMethod(t *testing.T) {
	for _, tc := range []struct{ in, svc, method string }{
		{"echo.Echo/Say", "echo.Echo", "Say"},
		{"/echo.Echo/Say", "echo.Echo", "Say"},
	} {
		svc, m, err := splitMethod(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.svc, svc)
		require.Equal(t, tc.method, m)
	}
	_, _, err := splitMethod("nope")
	require.Error(t, err)
}

// grpc.NewClient defaults to the dns resolver, so a scheme-less target must be
// pinned to passthrough or the custom dialer is never consulted and the call
// dies with "produced zero addresses" after a ~20s resolver timeout. A target
// that already names a registered resolver scheme must survive untouched.
func TestNormalizeTarget(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"host:port", "localhost:50051", "passthrough:///localhost:50051"},
		{"ipv4:port", "127.0.0.1:50051", "passthrough:///127.0.0.1:50051"},
		{"bare host", "api.example.com", "passthrough:///api.example.com"},
		{"bufconn stand-in", "bufnet", "passthrough:///bufnet"},
		{"explicit dns", "dns:///api.example.com:443", "dns:///api.example.com:443"},
		{"explicit passthrough", "passthrough:///bufnet", "passthrough:///bufnet"},
		{"unix socket", "unix:///var/run/svc.sock", "unix:///var/run/svc.sock"},
		{"uppercase scheme", "DNS:///api.example.com", "DNS:///api.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizeTarget(tc.in))
		})
	}
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) } // Q25
