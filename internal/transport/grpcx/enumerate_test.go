package grpcx

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
)

// petstoreProto is the shared generation fixture, addressed from this package.
const petstoreProto = "../../gen/testdata/proto/petstore.proto"

func TestEnumerateFromProtoFilesFindsBothMethods(t *testing.T) {
	infos, err := EnumerateFromProtoFiles([]string{petstoreProto})
	require.NoError(t, err)
	require.Len(t, infos, 2)

	byName := map[string]MethodInfo{}
	for _, i := range infos {
		byName[i.Method] = i
	}

	get := byName["GetPet"]
	require.Equal(t, "pkg.PetStore", string(get.Service))
	require.Equal(t, "pkg.PetStore/GetPet", get.Full)
	require.False(t, get.Streaming)
	require.Equal(t, "GetPetRequest", string(get.Input.Name()))

	require.True(t, byName["ListPets"].Streaming,
		"a server-streaming RPC must be flagged, so the generator can skip it")
}

func TestEnumerateFromProtoFilesReportsACompileError(t *testing.T) {
	_, err := EnumerateFromProtoFiles([]string{"testdata/does-not-exist.proto"})
	require.Error(t, err)
	require.True(t, core.IsConfigError(err),
		"a .proto that will not compile is the definition's fault, not the target's")
}

func TestEnumerateFromReflectionExcludesTheReflectionService(t *testing.T) {
	infos, err := EnumerateFromReflection(context.Background(), "bufnet", true, WithDialer(startEcho(t)))
	require.NoError(t, err)
	require.NotEmpty(t, infos)

	for _, i := range infos {
		require.NotContains(t, string(i.Service), "ServerReflection",
			"the reflection service is in every reflective server's list and is not the API under test")
	}

	// ...and the service that *is* the API under test came back whole.
	require.Equal(t, "echo.Echo/Say", infos[0].Full)
	require.False(t, infos[0].Streaming)
	require.Equal(t, "EchoRequest", string(infos[0].Input.Name()))
}

func TestEnumerateFromReflectionFailsClearlyOnANonReflectiveTarget(t *testing.T) {
	// A plain TCP listener that speaks no gRPC.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = EnumerateFromReflection(ctx, ln.Addr().String(), true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reflection",
		"the message must point at reflection, since passing .proto files is the way out")
}

// Both enumerators must agree about the same server, or a collection generated
// one way would disagree with one generated the other way.
func TestBothEnumeratorsClassifyTheSameServiceIdentically(t *testing.T) {
	fromReflect, err := EnumerateFromReflection(context.Background(), "bufnet", true, WithDialer(startEcho(t)))
	require.NoError(t, err)
	fromFiles, err := EnumerateFromProtoFiles([]string{"testdata/echo.proto"})
	require.NoError(t, err)

	require.Len(t, fromReflect, 1)
	require.Len(t, fromFiles, 1)
	require.Equal(t, fromFiles[0].Full, fromReflect[0].Full)
	require.Equal(t, fromFiles[0].Streaming, fromReflect[0].Streaming)
	require.Equal(t, string(fromFiles[0].Input.FullName()), string(fromReflect[0].Input.FullName()))
}
