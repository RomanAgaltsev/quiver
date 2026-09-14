package cli

import (
	"context"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/RomanAgaltsev/quiver/examples/local/server/app"
	"github.com/RomanAgaltsev/quiver/internal/collection"
	"github.com/RomanAgaltsev/quiver/internal/gen"
)

// specPath names a fixture from the gen package. The mapping fixtures live
// beside the code they exercise; duplicating them here would let the two copies
// disagree about what the generator does.
func specPath(name string) string { return filepath.Join("..", "gen", "testdata", name) }

// The test that matters most: the generated collection has to actually run.
//
// A file that looks plausible and that no executor accepts is the failure mode
// golden tests cannot see, and the MVP review's finding M12 — the shipped
// example proved only HTTP, and nothing tested it — is why this is not optional.
func TestGeneratedCollectionRunsAgainstTheLocalServer(t *testing.T) {
	srv := httptest.NewServer(app.NewHTTPHandler())
	defer srv.Close()

	// The generated collection reads its bearer token from the environment,
	// because a generated credential is always an {{env:...}} reference.
	t.Setenv("BEARERAUTH_TOKEN", app.LoginToken)

	dir := t.TempDir()
	out, errOut, code := run(t, "gen", "openapi", filepath.Join("testdata", "local-server.yaml"), "-o", dir)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	// Every generated request file must parse back through the loader qv run uses.
	require.NoError(t, eachRequestFile(dir, func(p string) error {
		_, lErr := collection.LoadRequest(p)
		return lErr
	}))

	// And the collection must execute green against the real handlers.
	out, errOut, code = run(t, "run", dir, "-V", "base="+srv.URL, "--quiet")
	require.Equal(t, 0, code, "the generated collection did not run clean\nstdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, errOut, "[PASS] ok", "every generated request asserts its declared status")
	require.NotContains(t, errOut, "[FAIL]")
}

// The generated collection must also fail honestly: with the wrong token the
// authenticated requests 401, and the declared status assertion has to catch it.
func TestGeneratedCollectionFailsWhenTheTokenIsWrong(t *testing.T) {
	srv := httptest.NewServer(app.NewHTTPHandler())
	defer srv.Close()
	t.Setenv("BEARERAUTH_TOKEN", "not-the-token")

	dir := t.TempDir()
	_, _, code := run(t, "gen", "openapi", filepath.Join("testdata", "local-server.yaml"), "-o", dir)
	require.Equal(t, 0, code)

	_, errOut, code := run(t, "run", filepath.Join(dir, "users"),
		"--collection", dir, "-V", "base="+srv.URL, "--quiet")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "[FAIL] ok")
}

func TestGenCheckExitsOneOnDrift(t *testing.T) {
	dir := t.TempDir()
	_, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir)
	require.Equal(t, 0, code)

	// Same spec, no changes: --check is clean and writes nothing.
	out, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir, "--check")
	require.Equal(t, 0, code, out)

	// A spec that adds operations drifts.
	out, _, code = run(t, "gen", "openapi", specPath("multi-method.yaml"), "-o", dir, "--check")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "would generate")

	// ...and --check really did write nothing: the drifting files are still absent.
	require.NoFileExists(t, filepath.Join(dir, "pets", "createPet.yaml"))
}

func TestGenUnreadableSpecExitsTwo(t *testing.T) {
	_, _, code := run(t, "gen", "openapi", specPath("does-not-exist.yaml"), "-o", t.TempDir())
	require.Equal(t, 2, code, "an unreadable spec is a config error, never a run failure")
}

func TestGenSwagger2ExitsTwo(t *testing.T) {
	_, errOut, code := run(t, "gen", "openapi", specPath("swagger2.yaml"), "-o", t.TempDir())
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "2.0", "the message must name the version to convert from")
}

func TestGenPrintsTheReport(t *testing.T) {
	dir := t.TempDir()
	out, _, code := run(t, "gen", "openapi", specPath("multi-method.yaml"), "-o", dir)
	require.Equal(t, 0, code)
	require.Contains(t, out, "generated")
	require.Contains(t, out, "4 file(s)", "three operations plus collection.yaml")
}

// The report is where a user learns a file was skipped. If it is not printed,
// the promise that edits are preserved is invisible and therefore untrustworthy.
func TestGenReportsSkippedAndOrphanedFiles(t *testing.T) {
	dir := t.TempDir()
	_, _, code := run(t, "gen", "openapi", specPath("multi-method.yaml"), "-o", dir)
	require.Equal(t, 0, code)

	edited := filepath.Join(dir, "pets", "listPets.yaml")
	original, err := os.ReadFile(edited)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(edited, append(original, []byte("\n# mine\n")...), 0o644))

	// minimal.yaml declares only listPets, so createPet and deletePet orphan.
	out, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir)
	require.Equal(t, 0, code, "a skip is not a failure")
	require.Contains(t, out, "hand-edited")
	require.Contains(t, out, "orphaned")
	require.Contains(t, out, "pets/createPet.yaml")

	after, err := os.ReadFile(edited)
	require.NoError(t, err)
	require.Contains(t, string(after), "# mine", "the edit survived")
	require.FileExists(t, filepath.Join(dir, "pets", "createPet.yaml"), "an orphan is never deleted")
}

func TestGenForceOverwritesAnEditedFile(t *testing.T) {
	dir := t.TempDir()
	_, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir)
	require.Equal(t, 0, code)

	edited := filepath.Join(dir, "pets", "listPets.yaml")
	require.NoError(t, os.WriteFile(edited, []byte("name: mine\nprotocol: http\nhttp:\n  method: GET\n  url: \"{{base}}/x\"\n"), 0o644))

	out, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir, "--force")
	require.Equal(t, 0, code)
	require.Contains(t, out, "generated 1 file(s)")

	after, err := os.ReadFile(edited)
	require.NoError(t, err)
	require.Contains(t, string(after), "name: listPets")
}

// The lockfile is the contract. A user who deletes it gets the conservative
// behaviour — everything is unmanaged, nothing is touched — rather than a tree
// silently overwritten.
func TestGenWithoutALockfileTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	_, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir)
	require.Equal(t, 0, code)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".qv")))

	out, _, code := run(t, "gen", "openapi", specPath("minimal.yaml"), "-o", dir)
	require.Equal(t, 0, code)
	require.Contains(t, out, "not generated by qv")
}

// protoFixture names a .proto fixture from the gen package's testdata.
func protoFixture(name string) string {
	return filepath.Join("..", "gen", "testdata", "proto", name)
}

// startLocalGRPCServer brings up the shipped example's gRPC service on a real
// TCP port with reflection enabled, and returns its address. A real port rather
// than bufconn because the generated collection names its target as a string
// and the CLI dials it by name.
func startLocalGRPCServer(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	app.RegisterGRPC(srv)
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})
	return lis.Addr().String()
}

func TestGenProtoFromFiles(t *testing.T) {
	dir := t.TempDir()
	out, errOut, code := run(t, "gen", "proto", protoFixture("petstore.proto"),
		"--target", "localhost:50052", "-o", dir)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	require.FileExists(t, filepath.Join(dir, "pkg-petstore", "GetPet.yaml"))
	require.FileExists(t, filepath.Join(dir, gen.CollectionFileName))

	c, err := os.ReadFile(filepath.Join(dir, gen.CollectionFileName))
	require.NoError(t, err)
	require.Contains(t, string(c), "grpc_target")
	require.Contains(t, string(c), "localhost:50052")
	require.NotContains(t, string(c), "auth:",
		"a .proto carries no security metadata; there is nothing to map")

	// The streaming RPC is skipped and said so.
	require.Contains(t, out, "ListPets")
	require.NoFileExists(t, filepath.Join(dir, "pkg-petstore", "ListPets.yaml"))
}

// proto_files is resolved relative to the REQUEST FILE, not the working
// directory. What matters is that the recorded path resolves by the loader's
// own rule, which is what this asserts rather than assuming a relative result:
// a collection generated into %TEMP% on C: from a .proto on E: has no relative
// path between them at all, and an absolute one is then the correct answer.
func TestGenProtoWritesAResolvableProtoFilesPath(t *testing.T) {
	dir := t.TempDir()
	_, _, code := run(t, "gen", "proto", protoFixture("petstore.proto"),
		"--target", "localhost:50052", "-o", dir)
	require.Equal(t, 0, code)

	r, err := collection.LoadRequest(filepath.Join(dir, "pkg-petstore", "GetPet.yaml"))
	require.NoError(t, err)
	require.Len(t, r.GRPC.ProtoFiles, 1)

	p := r.GRPC.ProtoFiles[0]
	require.Contains(t, p, "petstore.proto")
	if !filepath.IsAbs(p) {
		require.NotContains(t, p, `\`,
			"a relative path must use / so a collection generated on Windows runs on Linux")
		p = filepath.Join(filepath.Dir(r.Path), filepath.FromSlash(p))
	}
	require.FileExists(t, p, "the recorded proto_files path does not resolve from the request file")
}

func TestGenProtoRequiresATargetOrReflect(t *testing.T) {
	_, errOut, code := run(t, "gen", "proto", protoFixture("petstore.proto"), "-o", t.TempDir())
	require.Equal(t, 2, code,
		"a generated request needs a target; failing at config time is the package rule")
	require.Contains(t, errOut, "--target")
}

func TestGenProtoRejectsBothSources(t *testing.T) {
	_, errOut, code := run(t, "gen", "proto", "x.proto", "--reflect", "localhost:1", "-o", t.TempDir())
	require.Equal(t, 2, code, "files and --reflect are two sources for one run")
	require.Contains(t, errOut, "pick one")
}

func TestGenProtoNoSourceAtAllExitsTwo(t *testing.T) {
	_, _, code := run(t, "gen", "proto", "-o", t.TempDir())
	require.Equal(t, 2, code)
}

func TestGenProtoRejectsANonPositiveDepth(t *testing.T) {
	_, _, code := run(t, "gen", "proto", protoFixture("petstore.proto"),
		"--target", "x:1", "--depth", "0", "-o", t.TempDir())
	require.Equal(t, 2, code)
}

func TestGenProtoUnreadableProtoExitsTwo(t *testing.T) {
	_, _, code := run(t, "gen", "proto", "does-not-exist.proto", "--target", "x:1", "-o", t.TempDir())
	require.Equal(t, 2, code)
}

func TestGenProtoReflectRecordsAHashlessSource(t *testing.T) {
	addr := startLocalGRPCServer(t)
	dir := t.TempDir()

	_, errOut, code := run(t, "gen", "proto", "--reflect", addr, "--plaintext", "-o", dir)
	require.Equal(t, 0, code, errOut)

	lock, err := os.ReadFile(filepath.Join(dir, gen.LockPath))
	require.NoError(t, err)
	require.Contains(t, string(lock), "reflect://")
	require.NotContains(t, string(lock), "source_hash",
		"a live server has no stable hash; inventing one makes every restart look like drift")
}

// The test that matters most: a generated gRPC collection has to actually run.
//
// A wrong JSONName, a badly mapped well-known type or a bad relative proto_files
// path all produce output that parses, validates and pins happily into a
// golden — and that the server rejects.
func TestGeneratedGRPCCollectionRunsAgainstTheLocalServer(t *testing.T) {
	addr := startLocalGRPCServer(t)
	dir := t.TempDir()

	out, errOut, code := run(t, "gen", "proto", "--reflect", addr, "--plaintext", "-o", dir)
	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", out, errOut)

	require.NoError(t, eachRequestFile(dir, func(p string) error {
		_, err := collection.LoadRequest(p)
		return err
	}))

	out, errOut, code = run(t, "run", dir, "-V", "grpc_target="+addr, "--quiet")
	require.Equal(t, 0, code,
		"the generated gRPC collection did not run clean\nstdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, errOut, "[PASS] ok")
}

// eachRequestFile visits every generated request file in a collection tree,
// skipping collection.yaml and the .qv state directory. Both generators'
// end-to-end tests use it, so "every file loads" means the same thing for each.
func eachRequestFile(dir string, fn func(path string) error) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".yaml" || filepath.Base(p) == gen.CollectionFileName {
			return nil
		}
		return fn(p)
	})
}
