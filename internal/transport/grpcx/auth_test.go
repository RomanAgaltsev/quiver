package grpcx

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// An auth profile must reach the wire for gRPC, not only for HTTP. env.Resolve
// populates rr.Auth for every protocol, and grpcx used to ignore it: a request
// saying `auth: main` validated, resolved, ran, and sent nothing.
func TestGRPCAuthProfileReachesTheWire(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profile  request.AuthProfile
		seenKey  string
		wantSeen string
	}{
		{
			name:     "bearer",
			profile:  request.AuthProfile{Type: "bearer", Token: "t0k"},
			seenKey:  "X-Seen-Authorization",
			wantSeen: "Bearer t0k",
		},
		{
			name:     "basic",
			profile:  request.AuthProfile{Type: "basic", Username: "ada", Password: "pw"},
			seenKey:  "X-Seen-Authorization",
			wantSeen: "Basic " + base64.StdEncoding.EncodeToString([]byte("ada:pw")),
		},
		{
			name:     "apikey",
			profile:  request.AuthProfile{Type: "apikey", Header: "X-API-Key", Key: "k3y"},
			seenKey:  "X-Seen-X-Api-Key",
			wantSeen: "k3y",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := New(WithDialer(startEcho(t)))
			t.Cleanup(func() { _ = exec.(core.Closer).Close() })

			resp, err := exec.Execute(context.Background(), core.ResolvedRequest{
				Name: "e", Protocol: request.ProtocolGRPC,
				GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say",
					Message: `{"msg":"hi"}`, Plaintext: true},
				Auth: &tc.profile,
			})
			require.NoError(t, err)
			require.Equal(t, tc.wantSeen, resp.HeaderGet(tc.seenKey))
		})
	}
}

// An explicit metadata entry is more specific than the profile the request
// merely references, so it must win.
func TestGRPCExplicitMetadataBeatsAuthProfile(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	profile := request.AuthProfile{Type: "bearer", Token: "from-profile"}
	resp, err := exec.Execute(context.Background(), core.ResolvedRequest{
		Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say",
			Message:   `{"msg":"hi"}`,
			Metadata:  map[string]string{"authorization": "Bearer explicit"},
			Plaintext: true},
		Auth: &profile,
	})
	require.NoError(t, err)
	require.Equal(t, "Bearer explicit", resp.HeaderGet("X-Seen-Authorization"))
}

// An apikey profile with no header name cannot be sent at all, so it is a config
// error rather than a silent no-op that surfaces later as a 401.
func TestGRPCApikeyWithoutHeaderIsConfigError(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	profile := request.AuthProfile{Type: "apikey", Key: "k3y"}
	_, err := exec.Execute(context.Background(), core.ResolvedRequest{
		Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say",
			Message: `{"msg":"hi"}`, Plaintext: true},
		Auth: &profile,
	})
	require.Error(t, err)
	require.True(t, core.IsConfigError(err))
}

// A call that never reached a server is a transport failure — exactly like
// "connection refused" over HTTP — and must be an error, not an inspectable
// exit-0 response. The .proto path is used deliberately: reflection would fail
// first and mask the bug, which is why it survived the original test suite.
func TestGRPCDialFailureIsATransportError(t *testing.T) {
	exec := New(WithDialer(func(context.Context, string) (net.Conn, error) {
		return nil, errors.New("refused")
	}))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	resp, err := exec.Execute(context.Background(), core.ResolvedRequest{
		Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "echo.Echo/Say",
			Message: `{"msg":"hi"}`, Plaintext: true,
			ProtoFiles: []string{"testdata/echo.proto"}},
	})
	require.Error(t, err, "a dial failure must not be reported as an exit-0 response")
	require.Nil(t, resp)
	require.False(t, core.IsConfigError(err), "a dead target is a run failure (exit 1), not config")
}

// A malformed method name is caught before anything is dialled, so it is a
// usage error (exit 2) rather than a transport failure.
func TestGRPCMalformedMethodIsConfigError(t *testing.T) {
	exec := New(WithDialer(startEcho(t)))
	t.Cleanup(func() { _ = exec.(core.Closer).Close() })

	_, err := exec.Execute(context.Background(), core.ResolvedRequest{
		Name: "e", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "bufnet", Method: "notamethod", Plaintext: true},
	})
	require.Error(t, err)
	require.True(t, core.IsConfigError(err))
}
