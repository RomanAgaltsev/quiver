package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// run executes the root command with args and returns stdout, stderr and the
// process exit code the CLI would use. The returned stderr includes the final
// cause line, because Execute prints that to the real stderr after cobra returns
// (SilenceErrors keeps cobra itself quiet) and a test asserting on the message
// the user sees has to see the same text.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)

	err := cmd.Execute()
	if err == nil {
		return out.String(), errOut.String(), 0
	}
	stderr := errOut.String() + "qv: " + err.Error() + "\n"

	var ee *exitError
	if errors.As(err, &ee) {
		return out.String(), stderr, ee.Code()
	}
	return out.String(), stderr, 1
}

// Spec §8 and the README both name "unresolved variable" as an exit-2 case. It
// exited 1, because ExitCode collapsed every RunResult.Err into a flat run
// failure — and the only exit-2 test used a *parse* error, which is caught on
// the pre-run path.
func TestRunResolveErrorExitsTwo(t *testing.T) {
	dir := t.TempDir()
	bad := writeRequest(t, dir, "unresolved.yaml",
		"name: u\nprotocol: http\nhttp:\n  method: GET\n  url: \"{{nope}}/x\"\n")

	_, errOut, code := run(t, "run", bad)
	require.Equal(t, 2, code, "an unresolved variable is a config error")
	require.Contains(t, errOut, "nope")
}

func TestRunUnknownAuthProfileExitsTwo(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	bad := writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nauth: nope\nhttp:\n  method: GET\n  url: http://x\n")

	_, _, code := run(t, "run", bad)
	require.Equal(t, 2, code)
}

// An inline {{env:NAME}} in a request file is resolved like any other secret
// reference, and an unset one is a config error rather than literal text on the
// wire.
func TestRunInlineSecretRefIsResolvedAndRedacted(t *testing.T) {
	t.Setenv("QV_TEST_INLINE", "s3cret")
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	dir := t.TempDir()
	p := writeRequest(t, dir, "a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n"+
			"  headers:\n    Authorization: \"Bearer {{env:QV_TEST_INLINE}}\"\n")

	out, _, code := run(t, "run", p, "--output", "json", "-v")
	require.Equal(t, 0, code)
	require.Equal(t, "Bearer s3cret", seen, "the secret must actually be resolved")
	require.NotContains(t, out, "s3cret", "and redacted from output")
}

func TestRunUnsetInlineSecretRefExitsTwo(t *testing.T) {
	dir := t.TempDir()
	p := writeRequest(t, dir, "a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: http://example.invalid\n"+
			"  headers:\n    Authorization: \"Bearer {{env:QV_DEFINITELY_UNSET}}\"\n")

	_, errOut, code := run(t, "run", p)
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "QV_DEFINITELY_UNSET")
}

// A transport failure is exit 1, and the summary must not restate the exit code
// as a count of failed assertions.
func TestRunTransportFailureExitsOneWithAHonestSummary(t *testing.T) {
	dir := t.TempDir()
	p := writeRequest(t, dir, "a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: http://127.0.0.1:1/x\n")

	_, errOut, code := run(t, "run", p)
	require.Equal(t, 1, code)
	require.NotContains(t, errOut, "1 assertion(s) or request(s) failed")
	require.Contains(t, errOut, "1 request(s) errored")
}

// --dry-run sends nothing and, by extension, changes nothing on disk.
func TestDryRunCreatesNoState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"),
		[]byte("defaults:\n  base: http://x\n"), 0o644))
	p := writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: \"{{base}}/y\"\n")

	out, _, code := run(t, "run", p, "--dry-run")
	require.Equal(t, 0, code)
	require.Contains(t, out, "GET http://x/y")

	_, err := os.Stat(filepath.Join(dir, ".qv"))
	require.True(t, os.IsNotExist(err), "--dry-run must not create .qv/")
}

// --dry-run must print the URL that would actually be requested, query merge
// included, or the preview disagrees with the wire.
func TestDryRunShowsEffectiveURL(t *testing.T) {
	dir := t.TempDir()
	p := writeRequest(t, dir, "a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: http://x/y\n"+
			"  query:\n    page: \"2\"\n")

	out, _, code := run(t, "run", p, "--dry-run")
	require.Equal(t, 0, code)
	require.Contains(t, out, "GET http://x/y?page=2")
}

// --check-status turns a non-OK response into a failure without any assertions.
func TestCheckStatusFailsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	p := writeRequest(t, dir, "a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	_, _, code := run(t, "run", p, "--quiet")
	require.Equal(t, 0, code, "a non-2xx is an inspectable response by default")

	_, _, code = run(t, "run", p, "--quiet", "--check-status")
	require.Equal(t, 1, code)
}

// fail_on_error in collection.yaml is the file-level equivalent of
// --check-status and was never covered.
func TestCollectionFailOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"),
		[]byte("fail_on_error: true\n"), 0o644))
	p := writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	_, _, code := run(t, "run", p, "--quiet")
	require.Equal(t, 1, code)
}

// --show-secrets is the debugging escape hatch and had no test at all.
func TestShowSecretsDisablesRedaction(t *testing.T) {
	t.Setenv("QV_TEST_SHOW", "s3cret")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"),
		[]byte("defaults:\n  token: \"{{env:QV_TEST_SHOW}}\"\n"), 0o644))
	p := writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n"+
			"  headers:\n    Authorization: \"Bearer {{token}}\"\n")

	out, _, _ := run(t, "run", p, "--dry-run")
	require.NotContains(t, out, "s3cret")
	require.Contains(t, out, "***")

	out, _, _ = run(t, "run", p, "--dry-run", "--show-secrets")
	require.Contains(t, out, "s3cret")
}

// A collection root that is also a repository carries YAML that is not ours.
func TestRunFolderIgnoresUnrelatedYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	writeRequest(t, dir, ".github/workflows/ci.yml", "name: ci\non: [push]\n")
	writeRequest(t, dir, "Taskfile.yml", "version: '3'\ntasks: {}\n")
	writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	_, _, code := run(t, "run", dir, "--quiet")
	require.Equal(t, 0, code)
}

// An explicit --collection is an assertion by the user; a typo must say so here
// rather than surfacing as a wave of "unresolved variable" errors.
func TestCollectionFlagValidatesItsTarget(t *testing.T) {
	_, errOut, code := run(t, "http", "GET", "http://x", "--collection", filepath.Join(t.TempDir(), "nope"), "--dry-run")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "not a directory")

	empty := t.TempDir()
	_, errOut, code = run(t, "http", "GET", "http://x", "--collection", empty, "--dry-run")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "no collection.yaml")
}

// Request loading accepts .yaml and .yml, so environments must not be stricter.
func TestEnvironmentAcceptsYmlExtension(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	writeRequest(t, dir, "environments/dev.yml", "base: http://x\n")
	p := writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: \"{{base}}/y\"\n")

	out, _, code := run(t, "run", p, "--env", "dev", "--dry-run")
	require.Equal(t, 0, code)
	require.Contains(t, out, "GET http://x/y")
}

func TestUnknownEnvironmentIsAConfigError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	p := writeRequest(t, dir, "requests/a.yaml",
		"name: a\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n")

	_, errOut, code := run(t, "run", p, "--env", "nope", "--dry-run")
	require.Equal(t, 2, code)
	require.Contains(t, errOut, "nope")
}
