package cli

import (
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

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
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".yaml" || strings.Contains(p, ".qv") {
			return err
		}
		if filepath.Base(p) == gen.CollectionFileName {
			return nil
		}
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
