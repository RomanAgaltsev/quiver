package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RomanAgaltsev/quiver/internal/collection"
	"github.com/RomanAgaltsev/quiver/internal/env"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/stretchr/testify/require"
)

func TestInitCreatesCollectionLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-api")

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"init", dir})
	require.NoError(t, cmd.Execute())

	for _, p := range []string{"collection.yaml", ".gitignore", "environments/dev.yaml", "requests"} {
		_, err := os.Stat(filepath.Join(dir, p))
		require.NoError(t, err, "missing %s", p)
	}
	gi, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(gi), ".qv/")
}

func TestInitRefusesNonEmptyWithoutForce(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"init", dir})
	require.Error(t, cmd.Execute())
}

// --http was bound to a throwaway `new(bool)` and did nothing; only HTTP
// scaffolding existed at all.
func TestNewScaffoldsEachProtocol(t *testing.T) {
	for _, tc := range []struct{ flag, wants string }{
		{"--http", "protocol: http"},
		{"--grpc", "protocol: grpc"},
		{"--graphql", "protocol: graphql"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "r.yaml")

		cmd := newRootCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"new", path, tc.flag})
		require.NoError(t, cmd.Execute())

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(data), tc.wants)
	}
}

func TestNewRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.yaml")
	require.NoError(t, os.WriteFile(path, []byte("name: mine\n"), 0o644))

	cmd := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"new", path, "--http"})
	require.Error(t, cmd.Execute())

	data, _ := os.ReadFile(path)
	require.Equal(t, "name: mine\n", string(data)) // untouched
}

// env list must read the collection root, not the process CWD.
func TestEnvListUsesCollectionRoot(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "dev.yaml"), []byte("a: b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "prod.yaml"), []byte("a: c\n"), 0o644))

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"env", "list", "--collection", dir})
	require.NoError(t, cmd.Execute())
	require.Equal(t, []string{"dev", "prod"}, strings.Fields(out.String()))
}

// `env show` must not print a resolved secret.
func TestEnvShowRedacts(t *testing.T) {
	t.Setenv("QV_TEST_TOKEN", "s3cret")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "dev.yaml"),
		[]byte("token: \"{{env:QV_TEST_TOKEN}}\"\n"), 0o644))

	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"env", "show", "--collection", dir, "--env", "dev"})
	require.NoError(t, cmd.Execute())
	require.NotContains(t, out.String(), "s3cret")
	require.Contains(t, out.String(), "***")
}

// The whole point of the Record schema change.
func TestHistoryListAndReplay(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	reqPath := writeRequest(t, dir, "requests/ping.yaml",
		"name: ping\nprotocol: http\nhttp:\n  method: GET\n  url: \""+srv.URL+"\"\n")

	run := newRootCmd()
	run.SetOut(&bytes.Buffer{})
	run.SetErr(&bytes.Buffer{})
	run.SetArgs([]string{"run", reqPath, "--output", "raw"})
	require.NoError(t, run.Execute())
	require.Equal(t, 1, hits)

	var listOut bytes.Buffer
	list := newRootCmd()
	list.SetOut(&listOut)
	list.SetErr(&bytes.Buffer{})
	list.SetArgs([]string{"history", "list", "--collection", dir})
	require.NoError(t, list.Execute())
	require.Contains(t, listOut.String(), "ping")

	id := strings.Fields(listOut.String())[0]

	replay := newRootCmd()
	replay.SetOut(&bytes.Buffer{})
	replay.SetErr(&bytes.Buffer{})
	replay.SetArgs([]string{"history", "replay", id, "--collection", dir, "--output", "raw"})
	require.NoError(t, replay.Execute())
	require.Equal(t, 2, hits) // the request really was sent again
}

// Scaffolds must survive strict parsing and validation themselves — a template
// with a mistyped key would produce files that cannot even run (Q16).
func TestNewScaffoldsAreValidRequests(t *testing.T) {
	for name, tmpl := range map[string]string{
		"http":    httpRequestTemplate,
		"grpc":    grpcRequestTemplate,
		"graphql": graphqlRequestTemplate,
	} {
		req, err := request.Parse([]byte(tmpl))
		require.NoError(t, err, "%s scaffold must parse", name)
		require.NoError(t, req.Validate(), "%s scaffold must validate", name)
		require.Equal(t, name, string(req.Protocol))
	}
}

// The init templates must load with strict decoding too.
func TestInitTemplatesLoad(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte(collectionTemplate), 0o644))
	col, err := collection.Load(dir)
	require.NoError(t, err, "collection.yaml template must load")
	require.NotEmpty(t, col.Defaults["base"])

	envPath := filepath.Join(dir, "dev.yaml")
	require.NoError(t, os.WriteFile(envPath, []byte(envTemplate), 0o644))
	vars, err := env.LoadEnvironment(envPath)
	require.NoError(t, err, "environments/dev.yaml template must load")
	require.NotEmpty(t, vars["base"])
}
