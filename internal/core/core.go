// Package core holds the protocol-agnostic request/response contract.
package core

import (
	"context"
	"errors"
	"net/textproto"
	"time"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Response is normalized across all protocols so that capture, assert,
// history and render never branch on the protocol.
//
// Boundary note: this is a *request/response* contract, not a universal one.
// it covers the MVP and the planned load-testing phase, but gRPC streaming
// deliberately breaks it and will need its own model.
type Response struct {
	Protocol   request.Protocol
	Status     int    // HTTP status code or numeric gRPC code (OK == 0)
	StatusText string // "200 OK", or the gRPC code name ("OK", "NOT_FOUND")
	// OK carries success explicitly. For gRPC, Status==0 means OK and is also the
	// zero value, so callers must never infer success from Status alone.
	// For GraphQL, OK is false when the payload carries a non-empty `errors` array,
	// even though the HTTP status was 200.
	OK       bool
	Headers  map[string][]string // HTTP headers, or gRPC leading+trailing metadata
	Body     []byte              // raw HTTP body, or gRPC/GraphQL JSON bytes
	Duration time.Duration
}

// HeaderGet returns the first value for a header, case-insensitively.
func (r *Response) HeaderGet(name string) string {
	canonical := textproto.CanonicalMIMEHeaderKey(name)
	if v, ok := r.Headers[canonical]; ok && len(v) > 0 {
		return v[0]
	}
	for k, v := range r.Headers { // gRPC metadata keys are lowercase, not canonical
		if textproto.CanonicalMIMEHeaderKey(k) == canonical && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// ResolvedRequest is a Request with all templates expanded and auth resolved.
type ResolvedRequest struct {
	Name     string
	Protocol request.Protocol
	HTTP     *request.HTTPSpec
	GRPC     *request.GRPCSpec
	GraphQL  *request.GraphQLSpec
	Auth     *request.AuthProfile
	Timeout  time.Duration // 0 means the executor default
	Insecure bool          // TLS skip-verify, from --insecure
}

// Executor sends one resolved request and returns a normalized response.
type Executor interface {
	Execute(ctx context.Context, req ResolvedRequest) (*Response, error)
}

// ExecutorFunc adapts a function to Executor. This is the one test seam the
// design actually needs: graphqlx composes over an injected executor, so
// it can be tested without standing up an HTTP server.
type ExecutorFunc func(ctx context.Context, req ResolvedRequest) (*Response, error)

func (f ExecutorFunc) Execute(ctx context.Context, req ResolvedRequest) (*Response, error) {
	return f(ctx, req)
}

// Closer is implemented by executors that hold poolable resources — currently
// only grpcx, which caches client connections and resolved descriptors per
// target.
type Closer interface {
	Close() error
}

// Registry maps a protocol to its executor.
type Registry map[request.Protocol]Executor

// Close closes every executor that holds resources, joining any errors. Safe to
// call when no executor implements Closer.
func (reg Registry) Close() error {
	var errs []error
	seen := make(map[Executor]bool, len(reg))
	for _, ex := range reg {
		if seen[ex] { // the same executor may serve several protocols
			continue
		}
		seen[ex] = true
		if c, ok := ex.(Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
