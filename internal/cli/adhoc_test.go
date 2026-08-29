package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPAdHoc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http", "GET", srv.URL, "--output", "raw"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "pong")
}

// Header and query flags need *different* separators. Splitting on ":" first
// turned `-q next=https://x` into {"next=https": "//x"} — reproduced during review.
func TestAdHocHeaderAndQueryParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer a:b", r.Header.Get("Authorization"))
		require.Equal(t, "https://cb/x", r.URL.Query().Get("next"))
		require.Equal(t, "a:b", r.URL.Query().Get("filter"))
	}))
	defer srv.Close()

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http", "GET", srv.URL,
		"-H", "Authorization: Bearer a:b",
		"-q", "next=https://cb/x",
		"-q", "filter=a:b",
		"--output", "raw"})
	require.NoError(t, cmd.Execute())
}

// A malformed pair is a config error, not a silently dropped header.
func TestAdHocMalformedHeaderIsConfigError(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http", "GET", "http://example.invalid", "-H", "Authorization"})
	err := cmd.Execute()
	require.Error(t, err)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 2, ee.Code())
}

// Ad-hoc mode resolves --env/--var templates. Previously `qv http GET
// "{{base}}/users"` sent the literal "{{base}}" despite --env being a visible
// persistent flag on the command.
func TestAdHocResolvesEnvironmentTemplates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/users", r.URL.Path)
	}))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "dev.yaml"),
		[]byte("base: \""+srv.URL+"\"\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http", "GET", "{{base}}/users",
		"--collection", dir, "--env", "dev", "--output", "raw"})
	require.NoError(t, cmd.Execute())
}

// Ad-hoc mode can authenticate.
func TestAdHocBearerFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"http", "GET", srv.URL, "--bearer", "tok", "--output", "raw"})
	require.NoError(t, cmd.Execute())
}
