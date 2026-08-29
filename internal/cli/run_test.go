package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// writeRequest writes a request file and returns its path.
func writeRequest(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func TestRunCommandHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "ping.yaml",
		"name: ping\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n"+
			"assertions:\n  - from: status\n    op: eq\n    value: \"200\"\n")

	var out, errOut bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"run", reqPath, "--output", "raw"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), `"ok":true`)
}

func TestRunAssertionFailureExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "p.yaml",
		"name: p\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n"+
			"assertions:\n  - from: status\n    op: eq\n    value: \"200\"\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", reqPath})
	err := cmd.Execute()
	require.Error(t, err)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 1, ee.Code())
}

// A config error must exit 2 *and* say what was wrong.
func TestRunConfigErrorExitsTwoWithMessage(t *testing.T) {
	dir := t.TempDir()
	bad := writeRequest(t, dir, "bad.yaml", "name: b\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\nassertion: oops\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", bad})
	err := cmd.Execute()
	require.Error(t, err)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 2, ee.Code())
	require.NotEqual(t, "exit code 2", err.Error()) // the cause survives
	require.Contains(t, err.Error(), "assertion")
}

// A folder run follows `order`, not filenames.
func TestRunFolderFollowsOrder(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeRequest(t, dir, "requests/aaa.yaml",
		"name: aaa\nprotocol: http\norder: 20\nhttp:\n  method: GET\n  url: \""+srv.URL+"/second\"\n")
	writeRequest(t, dir, "requests/zzz.yaml",
		"name: zzz\nprotocol: http\norder: 10\nhttp:\n  method: GET\n  url: \""+srv.URL+"/first\"\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", filepath.Join(dir, "requests"), "--output", "raw"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, []string{"/first", "/second"}, seen)
}

// A secret must be redacted in output and in the history record.
func TestRunRedactsSecretsAndRecordsHistory(t *testing.T) {
	t.Setenv("QV_TEST_TOKEN", "s3cret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"echo":"` + r.Header.Get("Authorization") + `"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "dev.yaml"),
		[]byte("token: \"{{env:QV_TEST_TOKEN}}\"\n"), 0o644))
	writeRequest(t, dir, "requests/me.yaml",
		"name: me\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n"+
			"  headers:\n    Authorization: \"Bearer {{token}}\"\n")

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", filepath.Join(dir, "requests", "me.yaml"), "--env", "dev", "--output", "raw"})
	require.NoError(t, cmd.Execute())

	require.NotContains(t, out.String(), "s3cret")
	require.Contains(t, out.String(), "***")

	// Q6: history is actually written now.
	data, err := os.ReadFile(filepath.Join(dir, ".qv", "history", "history.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"name":"me"`)
	require.NotContains(t, string(data), "s3cret")
}

// --dry-run resolves and prints without sending.
func TestRunDryRunSendsNothing(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	defer srv.Close()

	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "p.yaml",
		"name: p\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"run", reqPath, "--dry-run"})
	require.NoError(t, cmd.Execute())
	require.Equal(t, 0, hits)
	require.Contains(t, out.String(), "GET "+srv.URL)
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
