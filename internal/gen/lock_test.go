package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

func sampleRequest() request.Request {
	return request.Request{
		Name:     "listPets",
		Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{
			Method:  "GET",
			URL:     "{{base}}/pets",
			Headers: map[string]string{"Accept": "application/json"},
		},
		Assertions: []request.Assertion{
			{Name: "ok", From: "status", Op: "eq", Value: request.Val("200")},
		},
	}
}

// One test per row of the re-generation table.

func TestDecideNewOperationIsWritten(t *testing.T) {
	require.Equal(t, actionWrite, decide("a.yaml", &Lock{Files: map[string]LockEntry{}}, nil, false, false))
}

func TestDecideUnchangedGeneratedFileIsRewritten(t *testing.T) {
	body := []byte("name: x\n")
	lock := &Lock{Files: map[string]LockEntry{"a.yaml": {Hash: hashBytes(body)}}}
	require.Equal(t, actionWrite, decide("a.yaml", lock, body, true, false))
}

func TestDecideHandEditedFileIsSkipped(t *testing.T) {
	lock := &Lock{Files: map[string]LockEntry{"a.yaml": {Hash: hashBytes([]byte("original"))}}}
	require.Equal(t, actionSkipEdited, decide("a.yaml", lock, []byte("edited"), true, false))
}

func TestDecideHandEditedFileIsOverwrittenWithForce(t *testing.T) {
	lock := &Lock{Files: map[string]LockEntry{"a.yaml": {Hash: hashBytes([]byte("original"))}}}
	require.Equal(t, actionWrite, decide("a.yaml", lock, []byte("edited"), true, true))
}

func TestDecideDeletedGeneratedFileIsRestored(t *testing.T) {
	lock := &Lock{Files: map[string]LockEntry{"a.yaml": {Hash: "sha256:whatever"}}}
	require.Equal(t, actionRestore, decide("a.yaml", lock, nil, false, false))
}

func TestDecideUnmanagedFileIsSkipped(t *testing.T) {
	lock := &Lock{Files: map[string]LockEntry{}}
	require.Equal(t, actionSkipUnmanaged, decide("a.yaml", lock, []byte("mine"), true, false),
		"a file the generator never wrote is not the generator's to touch")
}

func TestWriteIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	files := []GeneratedFile{{Path: "pets/listPets.yaml", Req: sampleRequest()}}

	lock := newLock("spec.yaml", "sha256:abc")
	r1, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)
	require.Contains(t, r1.Written, "pets/listPets.yaml")

	first, err := os.ReadFile(filepath.Join(dir, "pets/listPets.yaml"))
	require.NoError(t, err)
	firstLock, err := os.ReadFile(filepath.Join(dir, LockPath))
	require.NoError(t, err)

	r2, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)
	require.Empty(t, r2.SkippedEdited, "an unmodified re-run must not report skips")
	require.Empty(t, r2.Written, "a re-run that changes nothing has written nothing")
	require.False(t, r2.Changed(), "--check must be clean against an up-to-date tree")

	second, err := os.ReadFile(filepath.Join(dir, "pets/listPets.yaml"))
	require.NoError(t, err)
	require.Equal(t, first, second, "output churn makes every re-run a noisy diff")

	secondLock, err := os.ReadFile(filepath.Join(dir, LockPath))
	require.NoError(t, err)
	require.Equal(t, firstLock, secondLock,
		"a re-run that changed nothing must not restamp the lockfile either")
}

func TestWriteReportsOrphansAndNeverDeletesThem(t *testing.T) {
	dir := t.TempDir()
	lock := newLock("spec.yaml", "sha256:abc")

	_, err := Write(dir, []GeneratedFile{{Path: "gone.yaml", Req: sampleRequest()}}, collectionFile{}, lock, false)
	require.NoError(t, err)

	// The operation leaves the spec: nothing is generated for it this time.
	rep, err := Write(dir, nil, collectionFile{}, lock, false)
	require.NoError(t, err)
	require.Contains(t, rep.Orphaned, "gone.yaml")
	require.FileExists(t, filepath.Join(dir, "gone.yaml"),
		"deleting a file because an endpoint left the spec is the person's decision")
}

func TestWriteSkipsAHandEditedFileAndReportsIt(t *testing.T) {
	dir := t.TempDir()
	files := []GeneratedFile{{Path: "pets/listPets.yaml", Req: sampleRequest()}}
	lock := newLock("spec.yaml", "sha256:abc")

	_, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)

	edited := filepath.Join(dir, "pets/listPets.yaml")
	mine := []byte("name: listPets\nprotocol: http\nhttp:\n  method: GET\n  url: \"{{base}}/pets?mine=1\"\n")
	require.NoError(t, os.WriteFile(edited, mine, 0o644))

	rep, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)
	require.Contains(t, rep.SkippedEdited, "pets/listPets.yaml")

	after, err := os.ReadFile(edited)
	require.NoError(t, err)
	require.Equal(t, mine, after, "the promise is that an edit is never overwritten")

	// ...and --force is how you ask for it to be.
	rep, err = Write(dir, files, collectionFile{}, lock, true)
	require.NoError(t, err)
	require.Contains(t, rep.Written, "pets/listPets.yaml")
	after, err = os.ReadFile(edited)
	require.NoError(t, err)
	require.NotEqual(t, mine, after)
}

func TestWriteRestoresADeletedGeneratedFile(t *testing.T) {
	dir := t.TempDir()
	files := []GeneratedFile{{Path: "pets/listPets.yaml", Req: sampleRequest()}}
	lock := newLock("spec.yaml", "sha256:abc")

	_, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(dir, "pets/listPets.yaml")))

	rep, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)
	require.Contains(t, rep.Restored, "pets/listPets.yaml")
	require.FileExists(t, filepath.Join(dir, "pets/listPets.yaml"))
}

func TestWriteLeavesAnUnmanagedFileAlone(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pets"), 0o755))
	mine := []byte("name: mine\nprotocol: http\nhttp:\n  method: GET\n  url: \"{{base}}/mine\"\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pets/listPets.yaml"), mine, 0o644))

	files := []GeneratedFile{{Path: "pets/listPets.yaml", Req: sampleRequest()}}
	rep, err := Write(dir, files, collectionFile{}, newLock("spec.yaml", "sha256:abc"), false)
	require.NoError(t, err)
	require.Contains(t, rep.SkippedUnmanaged, "pets/listPets.yaml")

	after, err := os.ReadFile(filepath.Join(dir, "pets/listPets.yaml"))
	require.NoError(t, err)
	require.Equal(t, mine, after)
}

// The lockfile is round-tripped, not merely written: a re-run in a fresh
// process reads it back and has to reach the same decisions.
func TestLockRoundTrips(t *testing.T) {
	dir := t.TempDir()
	files := []GeneratedFile{{Path: "pets/listPets.yaml", Req: sampleRequest(), OperationID: "listPets"}}
	lock := newLock("openapi.yaml", "sha256:abc")
	lock.Generator = "qv test"

	_, err := Write(dir, files, collectionFile{}, lock, false)
	require.NoError(t, err)

	reloaded, err := LoadLock(dir)
	require.NoError(t, err)
	require.Equal(t, lockVersion, reloaded.Version)
	require.Equal(t, "openapi.yaml", reloaded.Source)
	require.Equal(t, "listPets", reloaded.Files["pets/listPets.yaml"].OperationID)
	require.Equal(t, lock.Files["pets/listPets.yaml"].Hash, reloaded.Files["pets/listPets.yaml"].Hash)

	rep, err := Write(dir, files, collectionFile{}, reloaded, false)
	require.NoError(t, err)
	require.False(t, rep.Changed())
}

func TestLoadLockRejectsANewerFormat(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".qv"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, LockPath),
		[]byte("version: 99\nfiles: {}\n"), 0o644))

	_, err := LoadLock(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "newer qv")
}

// Everything written has to survive a round trip through the loader `qv run`
// uses, byte for byte as it sits on disk.
func TestGeneratedFilesParseBackAsRequests(t *testing.T) {
	dir := t.TempDir()
	doc := mustLoad(t, "testdata/params.yaml")
	c, files, _ := Generate(doc)

	_, err := Write(dir, files, c, newLock("params.yaml", "sha256:abc"), false)
	require.NoError(t, err)

	for _, f := range files {
		data, rErr := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		require.NoError(t, rErr)
		parsed, pErr := request.Parse(data)
		require.NoError(t, pErr, "%s:\n%s", f.Path, data)
		require.NoError(t, parsed.Validate(), f.Path)
	}

	coll, err := os.ReadFile(filepath.Join(dir, CollectionFileName))
	require.NoError(t, err)
	require.Contains(t, string(coll), "base:")
}

// A generated request file must not carry a `timeout:` key at all. An empty one
// would be both a lie (the generator did not choose a timeout) and, depending on
// how it marshals, unparseable.
func TestGeneratedRequestHasNoTimeoutKey(t *testing.T) {
	data, err := marshalRequest(sampleRequest())
	require.NoError(t, err)
	require.NotContains(t, string(data), "timeout")
}

// Marshalling must be stable across runs: a map iterated in random order would
// make every re-generation a diff.
func TestMarshalledRequestIsStableAcrossRuns(t *testing.T) {
	req := sampleRequest()
	req.HTTP.Headers = map[string]string{"Accept": "application/json", "X-A": "1", "X-B": "2"}
	req.HTTP.Query = map[string]string{"z": "1", "a": "2", "m": "3"}

	first, err := marshalRequest(req)
	require.NoError(t, err)
	for range 20 {
		again, aErr := marshalRequest(req)
		require.NoError(t, aErr)
		require.Equal(t, string(first), string(again))
	}
	require.True(t, strings.Index(string(first), "a: ") < strings.Index(string(first), "z: "),
		"map keys must be emitted in a deterministic order")
}
