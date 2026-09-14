package grpcx

import (
	"context"
	"fmt"
	"strings"

	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/RomanAgaltsev/quiver/internal/core"
)

// MethodInfo is one RPC, reduced to what generation needs. Both descriptor
// sources produce it, so the generator never branches on where a service
// definition came from.
type MethodInfo struct {
	Service   protoreflect.FullName
	Method    string
	Full      string // pkg.Service/Method — the form GRPCSpec.Method takes
	Input     protoreflect.MessageDescriptor
	Streaming bool // client, server or bidi — any of the three
}

// reflectionServicePrefix names the service every reflective server exposes.
// It is not the API under test, and generating request files for it would put
// two files nobody wants at the top of every reflection-sourced collection.
const reflectionServicePrefix = "grpc.reflection."

// EnumerateFromProtoFiles compiles the given .proto files and lists every RPC
// they declare.
//
// It reuses the same compiler the executor's own file-based method resolution
// uses, so a service that resolves for `qv grpc` enumerates here identically.
func EnumerateFromProtoFiles(paths []string) ([]MethodInfo, error) {
	files, err := compileProtos(paths)
	if err != nil {
		return nil, err
	}

	var out []MethodInfo
	for _, f := range files {
		svcs := f.Services()
		for i := range svcs.Len() {
			out = append(out, methodsOf(svcs.Get(i))...)
		}
	}
	return out, nil
}

// EnumerateFromReflection lists every RPC a reflective server exposes.
//
// Options are accepted so tests can inject the same bufconn dialer the executor
// tests use; in production none are passed and the dialling, TLS and plaintext
// handling are the executor's own.
func EnumerateFromReflection(ctx context.Context, target string, plaintext bool, opts ...Option) ([]MethodInfo, error) {
	e := &executor{timeout: DefaultTimeout, conns: map[string]*conn{}}
	for _, opt := range opts {
		opt(e)
	}
	defer func() { _ = e.Close() }()

	c, err := e.dial(target, plaintext)
	if err != nil {
		return nil, err
	}

	// NewClientAuto negotiates v1 vs v1alpha, as resolveMethod does and for the
	// same reason: pinning v1 fails outright against servers exposing only
	// v1alpha.
	rc := grpcreflect.NewClientAuto(ctx, c.cc)
	defer rc.Reset()

	names, err := rc.ListServices()
	if err != nil {
		return nil, core.NewConfigError(fmt.Errorf(
			"grpcx: list services on %s via reflection: %w "+
				"(if the server has reflection disabled, pass the .proto files instead)", target, err))
	}

	var out []MethodInfo
	for _, name := range names {
		if strings.HasPrefix(name, reflectionServicePrefix) {
			continue
		}
		sd, rErr := rc.ResolveService(name)
		if rErr != nil {
			return nil, core.NewConfigError(fmt.Errorf(
				"grpcx: resolve service %q via reflection: %w", name, rErr))
		}
		// UnwrapService is the one boundary where the deprecated v1
		// protoreflect/desc types enter and immediately leave, matching
		// resolveMethod: everything downstream of here is v2.
		out = append(out, methodsOf(sd.UnwrapService())...)
	}
	return out, nil
}

// methodsOf is the one place a service descriptor becomes MethodInfo values.
//
// Both enumerators go through it so they cannot drift: a method classified
// streaming by one source and unary by the other would be a silent
// inconsistency, visible only as a request file that fails at send time.
func methodsOf(svc protoreflect.ServiceDescriptor) []MethodInfo {
	methods := svc.Methods()
	out := make([]MethodInfo, 0, methods.Len())
	for i := range methods.Len() {
		m := methods.Get(i)
		out = append(out, MethodInfo{
			Service: svc.FullName(),
			Method:  string(m.Name()),
			Full:    string(svc.FullName()) + "/" + string(m.Name()),
			Input:   m.Input(),
			// Any of the three streaming shapes disqualifies the RPC:
			// quiver's Response is a request/response contract.
			Streaming: m.IsStreamingClient() || m.IsStreamingServer(),
		})
	}
	return out
}
