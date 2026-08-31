// Package core holds the protocol-agnostic request/response contract.
package core

import (
	"context"
	"errors"
	"net/textproto"
	"reflect"
	"slices"
	"time"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// ConfigError marks a failure that is the request definition's fault rather
// than the target's: an unresolved variable, an unknown auth profile, an
// unreadable body_file, a malformed method name. Spec §8 maps these to exit 2,
// and CI depends on telling them apart from a genuine run failure — "the API is
// broken" and "someone typo'd a variable name" must not share an exit code.
type ConfigError struct{ Err error }

// NewConfigError wraps err as a configuration error. A nil err yields nil so
// callers can wrap unconditionally.
func NewConfigError(err error) error {
	if err == nil {
		return nil
	}
	return &ConfigError{Err: err}
}

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// IsConfigError reports whether err is, or wraps, a ConfigError.
func IsConfigError(err error) bool {
	var ce *ConfigError
	return errors.As(err, &ce)
}

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
	v, _ := r.HeaderPresent(name)
	return v
}

// HeaderPresent returns the first value for a header and whether it was sent at
// all. Presence and emptiness are different questions: a header explicitly set
// to "" is present, and reporting it as absent made `capture` disagree with
// `assert` about the same response.
func (r *Response) HeaderPresent(name string) (string, bool) {
	canonical := textproto.CanonicalMIMEHeaderKey(name)
	if v, ok := r.Headers[canonical]; ok && len(v) > 0 {
		return v[0], true
	}
	for k, v := range r.Headers { // gRPC metadata keys are lowercase, not canonical
		if textproto.CanonicalMIMEHeaderKey(k) == canonical && len(v) > 0 {
			return v[0], true
		}
	}
	return "", false
}

// ResolvedRequest is a Request with all templates expanded and auth resolved.
//
// There is deliberately no per-request Insecure field: TLS skip-verify is an
// executor-construction option (--insecure), and carrying an unread flag on the
// central contract only invites the next author to set it and expect an effect.
type ResolvedRequest struct {
	Name     string
	Protocol request.Protocol
	HTTP     *request.HTTPSpec
	GRPC     *request.GRPCSpec
	GraphQL  *request.GraphQLSpec
	Auth     *request.AuthProfile
	Timeout  time.Duration // 0 means the executor default
}

// Executor sends one resolved request and returns a normalized response.
type Executor interface {
	Execute(ctx context.Context, req ResolvedRequest) (*Response, error)
}

// ExecutorFunc adapts a function to Executor. This is the one test seam the
// design actually needs: graphqlx composes over an injected executor, so
// it can be tested without standing up an HTTP server.
type ExecutorFunc func(ctx context.Context, req ResolvedRequest) (*Response, error)

// Execute calls f.
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
//
// Deduplication is by Closer identity over a slice, not a map[Executor]bool: the
// same executor may serve several protocols (httpx backs both http and graphql)
// and must not be closed twice — but ExecutorFunc is a func type, which is
// unhashable, so using an Executor as a map key panics at runtime on exactly the
// test seam this package documents.
func (reg Registry) Close() error {
	var (
		errs []error
		seen []Closer
	)
	for _, ex := range reg {
		c, ok := ex.(Closer)
		if !ok {
			continue
		}
		// An uncomparable Closer cannot be deduplicated, but it also cannot be the
		// same value twice in a map, so closing it is safe.
		if reflect.TypeOf(c).Comparable() && slices.Contains(seen, c) {
			continue
		}
		seen = append(seen, c)
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
