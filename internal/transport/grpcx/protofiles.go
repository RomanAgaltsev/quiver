package grpcx

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/RomanAgaltsev/quiver/internal/core"
)

// protoCache memoizes compiled descriptor sets by file set, so a folder run of N
// gRPC requests sharing the same .proto files compiles them once.
var protoCache sync.Map // key: string (joined paths) -> linker.Files

// resolveFromProtoFiles compiles the given .proto files and returns the method
// descriptor for svc/method, bypassing server reflection entirely.
//
// This exists because disabling reflection in production is common security
// hygiene, so a reflection-only client cannot reach exactly the endpoints users
// most want to call.
func resolveFromProtoFiles(paths []string, full, svc, method string) (protoreflect.MethodDescriptor, error) {
	files, err := compileProtos(paths)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		// linker.File embeds protoreflect.FileDescriptor, so the compiler already
		// hands back v2 descriptors. Wrapping them into the deprecated v1
		// protoreflect/desc types just to look a service up would be a pure
		// downgrade — this path never talks to a server and needs no v1 at all.
		sd, ok := f.FindDescriptorByName(protoreflect.FullName(svc)).(protoreflect.ServiceDescriptor)
		if !ok {
			continue
		}
		md := sd.Methods().ByName(protoreflect.Name(method))
		if md == nil {
			return nil, core.NewConfigError(
				fmt.Errorf("grpcx: method %q not found in service %q (from %v)", method, svc, paths))
		}
		if md.IsStreamingClient() || md.IsStreamingServer() {
			return nil, core.NewConfigError(
				fmt.Errorf("grpcx: %s is a streaming RPC; quiver supports unary calls only", full))
		}
		return md, nil
	}
	return nil, core.NewConfigError(fmt.Errorf("grpcx: service %q not found in %v", svc, paths))
}

func compileProtos(paths []string) (linker.Files, error) {
	key := fmt.Sprint(paths)
	if cached, ok := protoCache.Load(key); ok {
		return cached.(linker.Files), nil
	}

	// Each file is resolved against its own directory as an import root, so a
	// request file can name "./api/user.proto" and its imports still resolve.
	roots := make([]string, 0, len(paths))
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		roots = append(roots, filepath.Dir(p))
		names = append(names, filepath.Base(p))
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: roots,
		}),
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := compiler.Compile(context.Background(), names...)
	if err != nil {
		// A .proto that does not compile is the definition's fault, not the
		// target's, and nothing has been dialled yet.
		return nil, core.NewConfigError(fmt.Errorf("grpcx: compile proto files %v: %w", paths, err))
	}
	protoCache.Store(key, files)
	return files, nil
}
