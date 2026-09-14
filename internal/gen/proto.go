package gen

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"

	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx"
)

// GRPCTargetVar is the collection variable every generated gRPC request points
// at, so one -V or one environment file redirects the whole collection.
const GRPCTargetVar = "grpc_target"

// ProtoOptions is what the CLI knows and the mapper needs.
type ProtoOptions struct {
	// ProtoFiles are ABSOLUTE paths to the .proto sources. They are rewritten
	// per generated file to be relative to that file, because grpc.proto_files
	// is resolved relative to the request file rather than to the working
	// directory. Empty for a reflection source, which resolves descriptors from
	// the server at run time.
	ProtoFiles []string
	// OutDir is the collection root the files will be written into. It is used
	// only to compute those relative paths; nothing here touches the disk.
	OutDir    string
	Plaintext bool
	Depth     int
}

// MapProto turns enumerated RPCs into request files, plus notes for the report.
func MapProto(infos []grpcx.MethodInfo, opts ProtoOptions) ([]GeneratedFile, []string, error) {
	if opts.Depth <= 0 {
		opts.Depth = DefaultProtoDepth
	}

	var files []GeneratedFile
	var notes []string
	var streaming []string

	for _, mi := range infos {
		if mi.Streaming {
			// Named, never silent: a user whose service is streaming-heavy would
			// otherwise see an almost-empty collection and assume generation
			// failed.
			streaming = append(streaming, mi.Full)
			continue
		}
		p := protoFilePath(mi)
		req, err := mapMethod(mi, relativeProtoFiles(opts.ProtoFiles, opts.OutDir, p), opts.Plaintext, opts.Depth)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, GeneratedFile{Path: p, Req: req, OperationID: mi.Full})
	}

	if len(streaming) > 0 {
		sort.Strings(streaming)
		for _, full := range streaming {
			notes = append(notes, fmt.Sprintf(
				"%s is a streaming RPC and was skipped; quiver is unary-only until Phase 7", full))
		}
	}
	return files, notes, nil
}

// protoFilePath groups by service, reusing 2a's slugify — which already
// collapses the dots in a fully qualified name into one path segment and makes
// `..` impossible.
func protoFilePath(mi grpcx.MethodInfo) string {
	return path.Join(slugify(string(mi.Service)), sanitize(mi.Method)+".yaml")
}

// mapMethod turns one RPC into a runnable request file.
func mapMethod(mi grpcx.MethodInfo, protoFiles []string, plaintext bool, depth int) (request.Request, error) {
	msg, err := skeleton(mi.Input, depth)
	if err != nil {
		return request.Request{}, err
	}

	return request.Request{
		// The service's bare name plus the method: `pkg.PetStore/GetPet` is the
		// wire identifier and already in grpc.method, so repeating the package
		// in the display name buys nothing.
		Name:     shortName(mi),
		Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{
			Target:     "{{" + GRPCTargetVar + "}}",
			Method:     mi.Full,
			Message:    msg,
			ProtoFiles: protoFiles,
			Plaintext:  plaintext,
		},
		Assertions: []request.Assertion{{
			Name: "ok",
			From: "status",
			Op:   "eq", // gRPC status NAMES work with eq only (MVP review M5)
			// "OK" rather than "0": both match, and the name is the one a reader
			// can check against the response without a codes table.
			Value: request.Val("OK"),
		}},
	}, nil
}

// shortName is `Service/Method` without the proto package.
func shortName(mi grpcx.MethodInfo) string {
	svc := string(mi.Service)
	if i := lastDot(svc); i >= 0 {
		svc = svc[i+1:]
	}
	return svc + "/" + mi.Method
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

// ProtoCollection is the collection file for a generated gRPC tree.
//
// A .proto carries no server address and no security metadata, so unlike the
// OpenAPI generator there is nothing to map into auth profiles: the one thing
// worth writing down is where to send the calls.
func ProtoCollection(target string) Collection {
	return Collection{Defaults: map[string]string{GRPCTargetVar: target}}
}

// relativeProtoFiles rewrites absolute .proto paths to be relative to the
// generated request file that will name them.
//
// grpc.proto_files is resolved relative to the request file, not to the working
// directory, so a path that is correct in the terminal that ran the generator is
// wrong everywhere else. Separators are forced to "/" so a collection generated
// on Windows still runs on Linux — the kind of thing that passes on the machine
// that wrote it and fails on every other one.
func relativeProtoFiles(abs []string, outDir, filePath string) []string {
	if len(abs) == 0 {
		return nil
	}
	dir := filepath.Dir(filepath.Join(outDir, filepath.FromSlash(filePath)))
	out := make([]string, 0, len(abs))
	for _, p := range abs {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			// Two different Windows volumes have no relative path between them —
			// a collection generated into %TEMP% on C: from a .proto on E: is the
			// ordinary case, not a corner one. An absolute path is then the only
			// correct answer, and the loader already honours it (env.go keeps an
			// absolute proto_files entry as-is and joins only relative ones).
			// Native separators are kept: this path is machine-specific by
			// definition, so there is nothing to make portable.
			out = append(out, p)
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}
