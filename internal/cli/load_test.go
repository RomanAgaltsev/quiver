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
)

func TestLoadCommandRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "ping.yaml",
		"name: ping\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"load", reqPath, "--rate", "500", "--requests", "50"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "requests")
	require.Contains(t, out.String(), "corrected")
}

func TestLoadCommandJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "ping.yaml",
		"name: ping\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"load", reqPath, "--rate", "500", "--requests", "20", "--output", "json"})
	require.NoError(t, cmd.Execute())

	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, float64(0), got["exit_code"])
}

func TestLoadThresholdFailureExitsOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "p.yaml",
		"name: p\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n"+
			"load:\n  rate: 500\n  requests: 20\n  thresholds:\n    error_rate: 0.0\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"load", reqPath})
	err := cmd.Execute()
	require.Error(t, err)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 1, ee.Code())
}

// Captures on a load target are a config error, before anything is sent.
func TestLoadRejectsCapturesWithExitTwo(t *testing.T) {
	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "c.yaml",
		"name: c\nprotocol: http\nhttp:\n  method: GET\n  url: http://127.0.0.1:1\n"+
			"captures:\n  - var: tok\n    from: body\n    path: token\n"+
			"load:\n  rate: 10\n  requests: 5\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"load", reqPath})
	err := cmd.Execute()
	require.Error(t, err)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 2, ee.Code())
	require.Contains(t, err.Error(), "tok")
}

// An incoherent profile is caught before sending, too.
func TestLoadRejectsProfileWithNoStopCondition(t *testing.T) {
	dir := t.TempDir()
	reqPath := writeRequest(t, dir, "n.yaml",
		"name: n\nprotocol: http\nhttp:\n  method: GET\n  url: http://127.0.0.1:1\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"load", reqPath, "--rate", "10"})
	err := cmd.Execute()
	require.Error(t, err)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 2, ee.Code())
	require.Contains(t, err.Error(), "duration or requests")
}

// The setup chain is reachable from the CLI and its captures reach the load.
func TestLoadWithSetupFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok"})
		case "/me":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(401)
			}
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	writeRequest(t, dir, "auth/login.yaml",
		"name: login\nprotocol: http\nhttp:\n  method: POST\n  url: \""+srv.URL+"/login\"\n"+
			"captures:\n  - var: tok\n    from: body\n    path: token\n")
	writeRequest(t, dir, "load/me.yaml",
		"name: me\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"/me\"\n"+
			"  headers:\n    Authorization: \"Bearer {{tok}}\"\n"+
			"load:\n  rate: 500\n  requests: 20\n  thresholds:\n    error_rate: 0.0\n")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"load", filepath.Join(dir, "load", "me.yaml"),
		"--setup", filepath.Join(dir, "auth")})
	require.NoError(t, cmd.Execute(), "every load request should have carried the token")
}
