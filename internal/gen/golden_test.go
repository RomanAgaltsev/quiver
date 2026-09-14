package gen

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx"
)

// -update rewrites the golden trees. Review the diff when you use it: a golden
// test whose goldens are refreshed unread asserts nothing.
var update = flag.Bool("update", false, "rewrite the golden output trees")

// goldenFixtures are the specs whose whole emitted tree is pinned. Between them
// they cover path/query/header parameters, bodies with and without examples,
// every security scheme kind, multiple servers, tagged and untagged operations,
// a missing operationId and an operation declaring no 2xx.
var goldenFixtures = []string{
	"minimal",
	"multi-method",
	"params",
	"statuses",
	"bodies",
	"security-header",
	"servers",
	"security-all",
	"untagged",
}

// The deliverable is the emitted text, so the test asserts on exactly that.
func TestGoldenTrees(t *testing.T) {
	for _, fixture := range goldenFixtures {
		t.Run(fixture, func(t *testing.T) {
			doc := mustLoad(t, filepath.Join("testdata", fixture+".yaml"))
			c, files, _ := Generate(doc)

			dir := t.TempDir()
			_, err := Write(dir, files, c, newLock(fixture+".yaml", "sha256:fixed"), false)
			require.NoError(t, err)

			// The lockfile carries a timestamp and a hash of the spec; it is
			// covered by its own round-trip test rather than pinned here.
			got := readTree(t, dir, ".qv")
			goldenDir := filepath.Join("testdata", "golden", fixture)

			if *update {
				require.NoError(t, os.RemoveAll(goldenDir))
				for path, data := range got {
					full := filepath.Join(goldenDir, filepath.FromSlash(path))
					require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
					require.NoError(t, os.WriteFile(full, data, 0o644))
				}
				t.Logf("updated %d file(s) under %s — read the diff", len(got), goldenDir)
				return
			}

			want := readTree(t, goldenDir)
			require.Equal(t, keysOf(want), keysOf(got),
				"the emitted file list changed; re-run with -update and read the diff")
			for path, data := range want {
				require.Equal(t, string(data), string(got[path]), path)
			}
		})
	}
}

// readTree reads every file under dir into a map keyed by slash-separated
// relative path, skipping the named directories.
func readTree(t *testing.T, dir string, skip ...string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(dir, p)
		if rErr != nil {
			return rErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			for _, s := range skip {
				if d.Name() == s {
					return filepath.SkipDir
				}
			}
			return nil
		}
		data, rErr := os.ReadFile(p)
		if rErr != nil {
			return rErr
		}
		// Golden files are checked out with the platform's line endings on some
		// configurations; comparing the text rather than the bytes keeps the
		// test honest about content without asserting the checkout's newlines.
		out[rel] = []byte(strings.ReplaceAll(string(data), "\r\n", "\n"))
		return nil
	}))
	return out
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// goldenProtoFixtures are the .proto files whose whole emitted tree is pinned.
// Between them they cover every scalar kind, repeated and map fields, an enum,
// a oneof, a well-known type, deep nesting, a self-referential message and a
// streaming RPC that must not appear at all.
var goldenProtoFixtures = []string{"petstore", "kinds", "deep", "cyclic"}

func TestGoldenProtoTrees(t *testing.T) {
	for _, fixture := range goldenProtoFixtures {
		t.Run(fixture, func(t *testing.T) {
			infos, err := grpcx.EnumerateFromProtoFiles([]string{protoFixture(fixture + ".proto")})
			require.NoError(t, err)

			// No ProtoFiles on purpose: a generated proto_files entry is a path
			// on the machine that ran the generator, and pinning one would make
			// the goldens fail everywhere else.
			files, _, err := MapProto(infos, ProtoOptions{Depth: DefaultProtoDepth})
			require.NoError(t, err)

			dir := t.TempDir()
			_, err = Write(dir, files, ProtoCollection("localhost:50051"),
				newLock(fixture+".proto", "sha256:fixed"), false)
			require.NoError(t, err)

			got := readTree(t, dir, ".qv")
			goldenDir := filepath.Join("testdata", "golden", "proto", fixture)

			if *update {
				require.NoError(t, os.RemoveAll(goldenDir))
				for path, data := range got {
					full := filepath.Join(goldenDir, filepath.FromSlash(path))
					require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
					require.NoError(t, os.WriteFile(full, data, 0o644))
				}
				t.Logf("updated %d file(s) under %s — read the diff", len(got), goldenDir)
				return
			}

			want := readTree(t, goldenDir)
			require.Equal(t, keysOf(want), keysOf(got),
				"the emitted file list changed; re-run with -update and read the diff")
			for path, data := range want {
				require.Equal(t, string(data), string(got[path]), path)
			}
		})
	}
}
