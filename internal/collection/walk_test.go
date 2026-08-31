package collection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// Spec §5 says a collection is "often its own git repo", and such a repo carries
// workflow, lint and compose YAML. Strict decoding turned every one of them into
// a hard error that blocked the whole folder run.
func TestListRequestsIgnoresNonRequestYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".github", "workflows", "ci.yml"), "name: ci\non: [push]\n")
	write(t, filepath.Join(dir, "Taskfile.yml"), "version: '3'\ntasks: {}\n")
	write(t, filepath.Join(dir, "docker-compose.yaml"), "services:\n  db:\n    image: postgres\n")
	write(t, filepath.Join(dir, "ok.yaml"),
		"name: ok\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n")

	reqs, err := ListRequests(dir)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, "ok", reqs[0].Name)
}

// Skipping .git, .qv and .github by name only ever catches the few we thought
// of; every dot-directory is tool state.
func TestListRequestsSkipsAllDotDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".idea", "thing.yaml"), "name: x\nprotocol: http\n")
	write(t, filepath.Join(dir, ".vscode", "thing.yml"), "name: y\nprotocol: http\n")
	write(t, filepath.Join(dir, "real.yaml"),
		"name: real\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n")

	reqs, err := ListRequests(dir)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, "real", reqs[0].Name)
}

// Strictness is kept exactly where it matters: a file that *does* declare
// protocol: and then has a typo still fails loudly.
func TestListRequestsStillRejectsBrokenRequestFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bad.yaml"),
		"name: bad\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\nassertion: oops\n")

	_, err := ListRequests(dir)
	require.Error(t, err)
	require.True(t, core.IsConfigError(err))
}

// Naming a file explicitly asserts that it is a request, so a bad one must be an
// error rather than a silent skip.
func TestListRequestsNamedFileIsAlwaysARequest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notarequest.yaml")
	write(t, p, "version: '3'\n")
	_, err := ListRequests(p)
	require.Error(t, err)
}

// An apikey profile with no header name is a silent no-op at send time; the
// symptom is a 401 that looks like a server problem.
func TestLoadRejectsUnusableAuthProfile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "collection.yaml"),
		"auth:\n  main:\n    type: apikey\n    key: k3y\n")
	_, err := Load(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "apikey")
	require.True(t, core.IsConfigError(err))
}

func TestLoadRejectsUnknownAuthType(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "collection.yaml"),
		"auth:\n  main:\n    type: oauth2\n")
	_, err := Load(dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "oauth2")
}

// Spec §5 documents a collection-level default timeout; it simply did not exist.
func TestLoadReadsCollectionTimeout(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "collection.yaml"), "timeout: 3s\n")
	c, err := Load(dir)
	require.NoError(t, err)
	require.Equal(t, "3s", c.Timeout.Duration().String())
}
