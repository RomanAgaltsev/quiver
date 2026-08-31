// Package httpx implements the HTTP executor.
package httpx

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// DefaultTimeout applies when neither the request nor an option sets one.
const DefaultTimeout = 30 * time.Second

type executor struct {
	client   *http.Client
	timeout  time.Duration
	insecure bool
}

// Option configures the executor.
type Option func(*executor)

// WithTimeout sets the default per-request timeout (--timeout).
func WithTimeout(d time.Duration) Option {
	return func(e *executor) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithInsecure disables TLS certificate verification (--insecure).
func WithInsecure(insecure bool) Option {
	return func(e *executor) { e.insecure = insecure }
}

// New returns an HTTP executor.
func New(opts ...Option) core.Executor {
	e := &executor{timeout: DefaultTimeout}
	for _, opt := range opts {
		opt(e)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if e.insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via --insecure
	}
	// No client-level Timeout: it is applied per request via the context, so a
	// request-level `timeout`: can override the default.
	e.client = &http.Client{Transport: tr}
	return e
}

func (e *executor) Execute(ctx context.Context, rr core.ResolvedRequest) (*core.Response, error) {
	spec := rr.HTTP
	if spec == nil {
		return nil, fmt.Errorf("httpx: missing http spec")
	}

	timeout := e.timeout
	if rr.Timeout > 0 {
		timeout = rr.Timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	target, err := spec.EffectiveURL()
	if err != nil {
		return nil, core.NewConfigError(fmt.Errorf("httpx: %w", err))
	}

	var body io.Reader
	if spec.Body != "" {
		body = strings.NewReader(spec.Body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(spec.Method), target, body)
	if err != nil {
		return nil, core.NewConfigError(fmt.Errorf("httpx: build request: %w", err))
	}

	for k, v := range spec.Headers {
		req.Header.Set(k, v)
	}
	// curl defaults a body to form encoding and HTTPie to JSON; sending *no*
	// content type is the one option that fails against most APIs. The user's own
	// header always wins — this only fills a gap.
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", sniffContentType(spec.Body))
	}
	// An explicit Authorization header is more specific than the profile the
	// request merely references, so it must not be silently overwritten.
	if req.Header.Get("Authorization") == "" {
		applyAuth(req, rr.Auth)
	}

	start := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httpx: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("httpx: read body: %w", err)
	}

	return &core.Response{
		Protocol:   rr.Protocol,
		Status:     resp.StatusCode,
		StatusText: resp.Status,
		OK:         resp.StatusCode < 400,
		Headers:    resp.Header,
		Body:       raw,
		Duration:   time.Since(start),
	}, nil
}

// sniffContentType guesses a content type from the body's shape. JSON is the
// overwhelming default for the APIs quiver targets; anything else is text until
// it looks like markup.
func sniffContentType(body string) string {
	if json.Valid([]byte(body)) {
		return "application/json"
	}
	if strings.HasPrefix(strings.TrimSpace(body), "<") {
		return "application/xml"
	}
	return "text/plain; charset=utf-8"
}

func applyAuth(req *http.Request, a *request.AuthProfile) {
	if a == nil {
		return
	}
	switch a.Type {
	case "basic":
		req.SetBasicAuth(a.Username, a.Password)
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+a.Token)
	case "apikey":
		if a.Header != "" {
			req.Header.Set(a.Header, a.Key)
		}
	}
}
