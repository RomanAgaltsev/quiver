package collection

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadCollection(t *testing.T) {
	c, err := Load(filepath.Join("testdata", "col"))
	require.NoError(t, err)
	require.Equal(t, "http://localhost", c.Defaults["base"])
	require.Equal(t, "bearer", c.Auth["main"].Type)
	require.False(t, c.FailOnError)
}

func TestLoadRequest(t *testing.T) {
	path := filepath.Join("testdata", "col", "requests", "list.yaml")
	r, err := LoadRequest(path)
	require.NoError(t, err)
	require.Equal(t, "list", r.Name)
	require.Equal(t, path, r.Path) // Q6: history/replay needs the source path
	require.NoError(t, r.Validate())
}

func TestLoadRequestInvalid(t *testing.T) {
	_, err := LoadRequest(filepath.Join("testdata", "does-not-exist.yaml"))
	require.Error(t, err)
}

// Folder runs follow `order`, not filenames. `login` (order 10) must run
// before `list` (order 20) even though "list.yaml" sorts first lexically, and the
// unordered request must come last despite "zz-" also sorting last by accident.
func TestListRequestsOrdersByOrderField(t *testing.T) {
	reqs, err := ListRequests(filepath.Join("testdata", "col", "requests"))
	require.NoError(t, err)
	names := make([]string, len(reqs))
	for i, r := range reqs {
		names[i] = r.Name
	}
	require.Equal(t, []string{"login", "list", "unordered"}, names)
}

func TestListRequestsSingleFile(t *testing.T) {
	reqs, err := ListRequests(filepath.Join("testdata", "col", "requests", "list.yaml"))
	require.NoError(t, err)
	require.Len(t, reqs, 1)
}

func TestListRequestsSkipsEnvironmentsAndState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "environments"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".qv"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "environments", "dev.yaml"), []byte("base: x\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".qv", "junk.yaml"), []byte("a: b\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "r.yaml"),
		[]byte("name: r\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n"), 0o644))

	reqs, err := ListRequests(dir)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, "r", reqs[0].Name)
}

// Root discovery must stop at a repository boundary rather than walking to
// the filesystem root and silently adopting a stray collection.yaml above the tree.
func TestFindRootStopsAtGitBoundary(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	nested := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	_, err := FindRoot(nested)
	require.Error(t, err) // no collection.yaml inside the repo; must not escape it
}

func TestFindRootFindsCollectionYAML(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "collection.yaml"), []byte("defaults: {}\n"), 0o644))
	nested := filepath.Join(root, "requests", "users")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	got, err := FindRoot(filepath.Join(nested, "list.yaml"))
	require.NoError(t, err)
	require.Equal(t, root, got)
}
