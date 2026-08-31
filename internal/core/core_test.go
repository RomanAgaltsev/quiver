package core

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

func TestResponseHeaderGet(t *testing.T) {
	resp := &Response{
		Protocol: request.ProtocolHTTP,
		Status:   200,
		OK:       true,
		Headers:  map[string][]string{"Content-Type": {"application/json"}},
		Body:     []byte(`{"ok":true}`),
		Duration: 5 * time.Millisecond,
	}
	require.Equal(t, "application/json", resp.HeaderGet("content-type"))
	require.Equal(t, "", resp.HeaderGet("missing"))
}

// gRPC metadata keys arrive lowercase, not canonicalized.
func TestResponseHeaderGetLowercaseMetadata(t *testing.T) {
	resp := &Response{Headers: map[string][]string{"x-request-id": {"abc"}}}
	require.Equal(t, "abc", resp.HeaderGet("X-Request-Id"))
}

// For gRPC, Status==0 means OK, which is also the zero value. OK disambiguates.
func TestResponseOKIsExplicit(t *testing.T) {
	grpcOK := &Response{Protocol: request.ProtocolGRPC, Status: 0, StatusText: "OK", OK: true}
	zero := &Response{}
	require.True(t, grpcOK.OK)
	require.False(t, zero.OK)
}

// Closing a registry closes every executor that holds resources, once.
type fakeCloser struct{ closed int }

func (f *fakeCloser) Execute(context.Context, ResolvedRequest) (*Response, error) { return nil, nil }
func (f *fakeCloser) Close() error                                                { f.closed++; return nil }

func TestRegistryCloseClosesClosers(t *testing.T) {
	fc := &fakeCloser{}
	reg := Registry{request.ProtocolGRPC: fc, request.ProtocolHTTP: noopExecutor{}}
	require.NoError(t, reg.Close())
	require.Equal(t, 1, fc.closed)
}

type noopExecutor struct{}

func (noopExecutor) Execute(context.Context, ResolvedRequest) (*Response, error) { return nil, nil }
