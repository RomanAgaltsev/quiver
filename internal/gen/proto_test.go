package gen

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx"
)

func protoFixture(name string) string { return filepath.Join("testdata", "proto", name) }

// mustMessage resolves one message descriptor out of a .proto fixture.
func mustMessage(t *testing.T, file, full string) protoreflect.MessageDescriptor {
	t.Helper()
	infos, err := grpcx.EnumerateFromProtoFiles([]string{file})
	require.NoError(t, err)
	require.NotEmpty(t, infos, "%s declares no service, so nothing can be enumerated from it", file)

	// Any descriptor reaches the whole file's type set through its parent file.
	fd := infos[0].Input.ParentFile()
	d := fd.Messages().ByName(protoreflect.Name(full[strings.LastIndex(full, ".")+1:]))
	require.NotNil(t, d, "%s has no message %s", file, full)
	require.Equal(t, full, string(d.FullName()))
	return d
}

func mustMethodInfo(t *testing.T, file, full string) grpcx.MethodInfo {
	t.Helper()
	infos, err := grpcx.EnumerateFromProtoFiles([]string{file})
	require.NoError(t, err)
	for _, mi := range infos {
		if mi.Full == full {
			return mi
		}
	}
	t.Fatalf("%s has no method %s", file, full)
	return grpcx.MethodInfo{}
}

// The defect this whole package is most likely to ship: protojson uses
// lowerCamelCase, so a proto `pet_id` must emit `petId`. snake_case parses as
// YAML, parses as JSON, and is rejected only on the wire.
func TestSkeletonUsesLowerCamelCaseFieldNames(t *testing.T) {
	md := mustMessage(t, protoFixture("petstore.proto"), "pkg.GetPetRequest")

	got, err := skeleton(md, 4)
	require.NoError(t, err)
	require.Contains(t, got, `"petId"`)
	require.NotContains(t, got, "pet_id")
	require.Contains(t, got, `"includePhotos": false`)
}

func TestSkeletonZeroValuesByKind(t *testing.T) {
	md := mustMessage(t, protoFixture("kinds.proto"), "pkg.AllKinds")

	got, err := skeleton(md, 4)
	require.NoError(t, err)
	for _, want := range []string{
		`"s": ""`, `"n": 0`, `"b": false`,
		`"rep": []`, `"m": {}`, `"d": 0`, `"raw": ""`,
	} {
		require.Contains(t, got, want, got)
	}
}

// A 64-bit integer is a JSON *string* in protojson, because a JSON number
// cannot hold one exactly. Emitting 0 produces a body the wire rejects — the
// same shape of defect as a snake_case field name, and just as invisible to
// every local check.
func TestSkeletonSixtyFourBitIntegersAreStrings(t *testing.T) {
	md := mustMessage(t, protoFixture("kinds.proto"), "pkg.AllKinds")
	got, err := skeleton(md, 4)
	require.NoError(t, err)
	require.Contains(t, got, `"big": "0"`, got)
}

func TestSkeletonEnumEmitsTheZeroValueName(t *testing.T) {
	md := mustMessage(t, protoFixture("kinds.proto"), "pkg.AllKinds")
	got, err := skeleton(md, 4)
	require.NoError(t, err)
	require.Contains(t, got, `"status": "STATUS_UNKNOWN"`,
		"protojson accepts the name; the number is legal but unreadable in a generated file")
}

func TestSkeletonOneofEmitsFirstMemberOnly(t *testing.T) {
	md := mustMessage(t, protoFixture("kinds.proto"), "pkg.WithOneof")
	got, err := skeleton(md, 4)
	require.NoError(t, err)
	require.Contains(t, got, `"byId"`)
	require.NotContains(t, got, `"byName"`,
		"a oneof with both members set is invalid; emit one")
	require.Contains(t, got, `"always"`,
		"a non-oneof field beside a oneof must still be emitted")
}

func TestSkeletonWellKnownTypesAreNotRecursedInto(t *testing.T) {
	md := mustMessage(t, protoFixture("petstore.proto"), "pkg.Pet")
	got, err := skeleton(md, 4)
	require.NoError(t, err)
	require.Contains(t, got, `"bornAt": ""`,
		"a Timestamp is an RFC 3339 string in protojson, not an object")
	require.NotContains(t, got, "seconds")
}

func TestSkeletonDepthCapStopsRecursion(t *testing.T) {
	md := mustMessage(t, protoFixture("deep.proto"), "pkg.L1")
	got, err := skeleton(md, 2)
	require.NoError(t, err)
	require.Contains(t, got, `"l2"`)
	require.NotContains(t, got, `"l4"`, "recursion must stop at the cap")
}

func TestSkeletonCycleGuardIsIndependentOfTheDepthCap(t *testing.T) {
	md := mustMessage(t, protoFixture("cyclic.proto"), "pkg.Node")

	// A large cap must still terminate: the cycle guard is a correctness
	// control, the cap is a size control, and conflating them means raising
	// --depth reintroduces a hang.
	done := make(chan struct{})
	go func() {
		got, err := skeleton(md, 1000)
		require.NoError(t, err)
		require.Contains(t, got, `"next": {}`)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("skeleton did not terminate on a self-referential message")
	}
}

func TestMapMethodShape(t *testing.T) {
	mi := mustMethodInfo(t, protoFixture("petstore.proto"), "pkg.PetStore/GetPet")

	req, err := mapMethod(mi, []string{"../proto/petstore.proto"}, true, 4)
	require.NoError(t, err)
	require.Equal(t, request.ProtocolGRPC, req.Protocol)
	require.Equal(t, "PetStore/GetPet", req.Name)
	require.Equal(t, "{{grpc_target}}", req.GRPC.Target)
	require.Equal(t, "pkg.PetStore/GetPet", req.GRPC.Method)
	require.True(t, req.GRPC.Plaintext)
	require.Equal(t, []string{"../proto/petstore.proto"}, req.GRPC.ProtoFiles)

	require.Len(t, req.Assertions, 1)
	require.Equal(t, "status", req.Assertions[0].From)
	require.Equal(t, "eq", req.Assertions[0].Op, "gRPC status names work with eq only (M5)")
	require.Equal(t, "OK", req.Assertions[0].Operand())

	require.NoError(t, req.Validate())
}

func TestMapProtoSkipsStreamingAndNamesIt(t *testing.T) {
	infos, err := grpcx.EnumerateFromProtoFiles([]string{protoFixture("petstore.proto")})
	require.NoError(t, err)

	files, notes, err := MapProto(infos, ProtoOptions{Depth: 4})
	require.NoError(t, err)
	require.Len(t, files, 1, "only the unary RPC is generated")
	require.Contains(t, strings.Join(notes, "\n"), "ListPets",
		"a silent omission is indistinguishable from a bug")
}

func TestMapProtoFilePathGroupsByService(t *testing.T) {
	infos, err := grpcx.EnumerateFromProtoFiles([]string{protoFixture("petstore.proto")})
	require.NoError(t, err)

	files, _, err := MapProto(infos, ProtoOptions{Depth: 4})
	require.NoError(t, err)
	require.Equal(t, "pkg-petstore/GetPet.yaml", files[0].Path)
}

// Every generated gRPC request must be a legal request file, and must survive
// the marshaller that writes it.
func TestMappedRPCsAreValidRequests(t *testing.T) {
	for _, fixture := range []string{"petstore.proto", "kinds.proto", "cyclic.proto", "deep.proto"} {
		infos, err := grpcx.EnumerateFromProtoFiles([]string{protoFixture(fixture)})
		if len(infos) == 0 {
			continue // a fixture with no service declares nothing to generate
		}
		require.NoError(t, err)

		files, _, err := MapProto(infos, ProtoOptions{Depth: 4})
		require.NoError(t, err)
		for _, f := range files {
			require.NoError(t, f.Req.Validate(), "%s: %s", fixture, f.Path)
			data, mErr := marshalRequest(f.Req)
			require.NoError(t, mErr, "%s: %s", fixture, f.Path)
			require.Contains(t, string(data), "protocol: grpc")
		}
	}
}
