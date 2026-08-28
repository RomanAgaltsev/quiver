package grpcx

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func TestResolveFromProtoFiles(t *testing.T) {
	md, err := resolveFromProtoFiles(
		[]string{filepath.Join("testdata", "echo.proto")},
		"echo.Echo/Say", "echo.Echo", "Say",
	)
	require.NoError(t, err)
	require.Equal(t, "Say", string(md.Name()))
	require.Equal(t, "echo.EchoRequest", string(md.Input().FullName()))
}

func TestResolveFromProtoFilesUnknownService(t *testing.T) {
	_, err := resolveFromProtoFiles(
		[]string{filepath.Join("testdata", "echo.proto")}, "echo.Nope/Say", "echo.Nope", "Say")
	require.Error(t, err)
}

func TestResolveFromProtoFilesMissingFile(t *testing.T) {
	_, err := resolveFromProtoFiles([]string{"testdata/nope.proto"}, "a.B/C", "a.B", "C")
	require.Error(t, err)
}

// End-to-end: the same bufconn server, but with reflection bypassed entirely.
func TestGRPCUnaryViaProtoFiles(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	rr := core.ResolvedRequest{Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{
			Target: "bufnet", Method: "echo.Echo/Say", Message: `{"msg":"hi"}`,
			Plaintext: true, ProtoFiles: []string{filepath.Join("testdata", "echo.proto")},
		}}
	resp, err := exec.Execute(context.Background(), rr)
	require.NoError(t, err)
	require.True(t, resp.OK)
	require.Contains(t, string(resp.Body), `"got:hi"`)
}
