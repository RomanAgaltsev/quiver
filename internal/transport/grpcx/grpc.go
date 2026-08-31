// Package grpcx implements the gRPC unary executor. Streaming is out of scope.
package grpcx

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// DefaultTimeout applies when neither the request nor an option sets one.
const DefaultTimeout = 30 * time.Second

type dialerFunc func(context.Context, string) (net.Conn, error)

// conn is a cached client connection plus its resolved method descriptors.
type conn struct {
	cc      *grpc.ClientConn
	mu      sync.Mutex
	methods map[string]protoreflect.MethodDescriptor // keyed by "pkg.Service/Method"
}

type executor struct {
	dialer   dialerFunc
	timeout  time.Duration
	insecure bool

	mu    sync.Mutex
	conns map[string]*conn // keyed by target + credential mode
}

// Option configures the executor.
type Option func(*executor)

// WithDialer injects a custom dialer (used in tests with bufconn).
func WithDialer(d dialerFunc) Option {
	return func(e *executor) {
		e.dialer = d
	}
}

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
	return func(e *executor) {
		e.insecure = insecure
	}
}

// New returns a gRPC unary executor. It implements core.Closer.
func New(opts ...Option) core.Executor {
	e := &executor{timeout: DefaultTimeout, conns: map[string]*conn{}}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// connCount reports how many connections are cached (test helper for Q41).
func (e *executor) connCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.conns)
}

// Close releases every cached connection (core.Closer, Q42).
func (e *executor) Close() error {
	e.mu.Lock()
	conns := e.conns
	e.conns = map[string]*conn{}
	e.mu.Unlock()

	for _, c := range conns {
		_ = c.cc.Close()
	}
	return nil
}

// dial returns a cached connection for the target, creating one if needed.
//
// Connections and resolved descriptors are cached per target because the previous
// revision dialled and ran a full reflection round-trip on *every* request (Q41).
func (e *executor) dial(target string, plaintext bool) (*conn, error) {
	key := fmt.Sprintf("%s|plaintext=%t|insecure=%t", target, plaintext, e.insecure)

	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.conns[key]; ok {
		return c, nil
	}

	// honour Plaintext and support TLS. The previous revision hardcoded
	// insecure credentials, so a TLS endpoint could not be called at all.
	var creds credentials.TransportCredentials
	if plaintext {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(&tls.Config{InsecureSkipVerify: e.insecure}) //nolint:gosec // opt-in via --insecure
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if e.dialer != nil {
		opts = append(opts, grpc.WithContextDialer(e.dialer))
	}
	cc, err := grpc.NewClient(normalizeTarget(target), opts...)
	if err != nil {
		return nil, fmt.Errorf("grpcx: dial %s: %w", target, err)
	}
	c := &conn{cc: cc, methods: map[string]protoreflect.MethodDescriptor{}}
	e.conns[key] = c
	return c, nil
}

// normalizeTarget prefixes a scheme-less target with "passthrough:///".
//
// grpc.NewClient (unlike the deprecated grpc.Dial) defaults to the *dns*
// resolver, so a bare "host:port" becomes "dns:///host:port". That silently
// breaks two things: a custom dialer is never consulted, because the resolver
// must produce an address before anything dials, and a target DNS cannot answer
// fails after a ~20s resolver timeout with "produced zero addresses" instead of
// a prompt connection error. quiver dials the address the user wrote — the same
// thing grpcurl does — so passthrough is the correct default.
//
// A target that already names a registered resolver scheme (dns://, unix://,
// passthrough://, ...) is left exactly as written. The registered-scheme check
// mirrors grpc-go's own parsing: "localhost:50051" parses as a URL with scheme
// "localhost", which is not a resolver, so it must still be prefixed.
func normalizeTarget(target string) string {
	if u, err := url.Parse(target); err == nil && u.Scheme != "" {
		if resolver.Get(strings.ToLower(u.Scheme)) != nil {
			return target
		}
	}
	return "passthrough:///" + target
}

// resolveMethod returns the method descriptor, using the per-connection cache and
// falling back to server reflection.
func (c *conn) resolveMethod(ctx context.Context, full, svc, method string) (protoreflect.MethodDescriptor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if md, ok := c.methods[full]; ok {
		return md, nil
	}

	// NewClientAuto negotiates v1 vs v1alpha. Pinning v1 (as the previous
	// revision did) fails outright against servers exposing only v1alpha —
	// older grpc-go and several non-Go runtimes.
	rc := grpcreflect.NewClientAuto(ctx, c.cc)
	defer rc.Reset()

	sd, err := rc.ResolveService(svc)
	if err != nil {
		return nil, fmt.Errorf("grpcx: resolve service %q via reflection: %w "+
			"(if the server has reflection disabled, set grpc.proto_files)", svc, err)
	}
	// UnwrapService is the single boundary where the deprecated v1
	// protoreflect/desc types enter and immediately leave. Everything the
	// executor caches and passes around is v2, so swapping the reflection
	// client for jhump/protoreflect/v2 later touches only these lines.
	md := sd.UnwrapService().Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil, fmt.Errorf("grpcx: method %q not found in service %q", method, svc)
	}
	if md.IsStreamingClient() || md.IsStreamingServer() {
		return nil, fmt.Errorf("grpcx: %s is a streaming RPC; quiver supports unary calls only", full)
	}
	c.methods[full] = md
	return md, nil
}

func (e *executor) Execute(ctx context.Context, rr core.ResolvedRequest) (*core.Response, error) {
	spec := rr.GRPC
	if spec == nil {
		return nil, fmt.Errorf("grpcx: missing grpc spec")
	}
	svc, method, err := splitMethod(spec.Method)
	if err != nil {
		return nil, err
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

	c, err := e.dial(spec.Target, spec.Plaintext)
	if err != nil {
		return nil, err
	}

	md, err := e.methodDescriptor(ctx, c, spec, svc, method)
	if err != nil {
		return nil, err
	}

	// Build the request message on dynamicpb (not protoreflect v1's legacy
	// `dynamic` package) so protojson governs both directions — see Q20 / Q31.
	reqMsg := dynamicpb.NewMessage(md.Input())
	if spec.Message != "" {
		if err := (protojson.UnmarshalOptions{DiscardUnknown: false}).
			Unmarshal([]byte(spec.Message), reqMsg); err != nil {
			// The message in the file does not fit the schema: the definition is
			// wrong, so this is exit 2 and nothing is sent.
			return nil, core.NewConfigError(fmt.Errorf("grpcx: request message JSON: %w", err))
		}
	}

	// Merged through a map, not appended: AppendToOutgoingContext *adds* a value
	// for a duplicate key rather than replacing it, and the server would then read
	// the first — the profile — even though the request named the header itself.
	// The request file is the more specific of the two, so it wins.
	authPairs, err := authMetadata(rr.Auth)
	if err != nil {
		return nil, err
	}
	send := make(map[string]string, len(spec.Metadata)+1)
	for i := 0; i+1 < len(authPairs); i += 2 {
		send[authPairs[i]] = authPairs[i+1]
	}
	for k, v := range spec.Metadata {
		send[strings.ToLower(k)] = v
	}
	if len(send) > 0 {
		pairs := make([]string, 0, len(send)*2)
		// Sorted, so the outgoing metadata is deterministic run to run.
		keys := make([]string, 0, len(send))
		for k := range send {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			pairs = append(pairs, k, send[k])
		}
		ctx = metadata.AppendToOutgoingContext(ctx, pairs...)
	}

	// Ask for leading and trailing metadata, or Response.Headers stays empty
	// and `from: header` captures/assertions silently return nothing for gRPC.
	var hdr, tlr metadata.MD

	// peer stays empty when the call never reached a server, which is how a dial
	// failure is told apart from a status the server chose to return. httpx
	// reports the same event as an error, and the two protocols must agree.
	var pr peer.Peer

	// Invoke on the ClientConn directly with a dynamicpb reply rather than
	// through grpcdynamic.Stub. The stub builds its reply with jhump's own
	// message factory, which returns a *dynamic.Message — a protobuf *v1*
	// message with no ProtoReflect method. It can therefore never satisfy the
	// protojson path below, so every successful unary call failed with
	// "unexpected reply type" and the Q20 EmitUnpopulated handling was dead.
	respMsg := dynamicpb.NewMessage(md.Output())
	fullMethod := "/" + string(md.FullName().Parent()) + "/" + string(md.Name())
	start := time.Now()
	callErr := c.cc.Invoke(ctx, fullMethod, reqMsg, respMsg,
		grpc.Header(&hdr), grpc.Trailer(&tlr), grpc.Peer(&pr))
	dur := time.Since(start)

	resp := &core.Response{Protocol: rr.Protocol, Duration: dur, Headers: map[string][]string{}}
	for k, v := range hdr {
		resp.Headers[k] = append(resp.Headers[k], v...)
	}
	for k, v := range tlr {
		resp.Headers[k] = append(resp.Headers[k], v...)
	}

	if callErr != nil {
		if pr.Addr == nil {
			// The call never reached a server: a transport failure, exactly like
			// "connection refused" over HTTP, and it must exit 1 the way httpx does.
			// Returning it as an inspectable response made a gRPC target that was
			// simply *down* report exit 0 — a green pipeline that should be red.
			return nil, fmt.Errorf("grpcx: %s: %w", spec.Target, callErr)
		}
		st, _ := status.FromError(callErr)
		// A status the server chose to return is a normal, inspectable response,
		// the same way an HTTP 404 is; --check-status / assertions decide whether
		// it fails the run.
		resp.Status = int(st.Code())
		resp.StatusText = st.Code().String()
		resp.OK = false
		body, mErr := protojson.Marshal(st.Proto())
		if mErr != nil {
			body = []byte(fmt.Sprintf(`{"code":%d,"message":%q}`, st.Code(), st.Message()))
		}
		resp.Body = body
		return resp, nil
	}

	resp.Status = int(codes.OK)
	resp.StatusText = codes.OK.String()
	resp.OK = true

	// EmitUnpopulated keeps zero-valued fields in the JSON. Without it a
	// reply of {count: 0, name: ""} marshals to {} and a capture on `count`
	// fails with "path not found" for no discoverable reason. This is exactly
	// what grpcurl's -emit-defaults exists for.
	jsonBytes, err := (protojson.MarshalOptions{EmitUnpopulated: true, Multiline: false}).
		Marshal(respMsg)
	if err != nil {
		return nil, fmt.Errorf("grpcx: marshal reply: %w", err)
	}
	resp.Body = jsonBytes
	return resp, nil
}

// methodDescriptor resolves via local .proto files when provided, otherwise via
// server reflection. The proto-file path is implemented in Task 10.
func (e *executor) methodDescriptor(ctx context.Context, c *conn, spec *request.GRPCSpec, svc, method string) (protoreflect.MethodDescriptor, error) {
	if len(spec.ProtoFiles) > 0 {
		return resolveFromProtoFiles(spec.ProtoFiles, spec.Method, svc, method) // Task 10
	}
	return c.resolveMethod(ctx, spec.Method, svc, method)
}

func splitMethod(full string) (service, method string, err error) {
	full = strings.TrimPrefix(full, "/")
	i := strings.LastIndex(full, "/")
	if i < 0 {
		// A malformed method name is a usage error, not a transport failure: it is
		// caught before anything is dialled, so it exits 2.
		return "", "", core.NewConfigError(
			fmt.Errorf("grpcx: method %q must be pkg.Service/Method", full))
	}
	return full[:i], full[i+1:], nil
}

// authMetadata maps an auth profile onto gRPC metadata pairs. bearer and apikey
// have direct equivalents; basic is the HTTP scheme carried in the same
// `authorization` key, which is what grpcurl and Envoy expect.
//
// Without this, a request file saying `auth: main` under `protocol: grpc`
// validated, resolved, ran — and sent nothing.
func authMetadata(a *request.AuthProfile) ([]string, error) {
	if a == nil {
		return nil, nil
	}
	switch a.Type {
	case "bearer":
		return []string{"authorization", "Bearer " + a.Token}, nil
	case "basic":
		enc := base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password))
		return []string{"authorization", "Basic " + enc}, nil
	case "apikey":
		if a.Header == "" {
			return nil, core.NewConfigError(
				fmt.Errorf("grpcx: auth profile of type apikey requires a header name"))
		}
		// Metadata keys are lower-case on the wire; AppendToOutgoingContext would
		// do this anyway, but doing it here keeps the pairs inspectable.
		return []string{strings.ToLower(a.Header), a.Key}, nil
	default:
		return nil, core.NewConfigError(fmt.Errorf("grpcx: unknown auth type %q", a.Type))
	}
}
