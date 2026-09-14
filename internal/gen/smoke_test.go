package gen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// realSpecs are unmodified third-party documents (see testdata/README.md).
//
// Fixtures prove the mapping; a real spec proves the mapping survives contact
// with one. These two between them carry shared $refs, operations with no
// operationId, oauth2 and mutualTLS schemes, a media type that is not JSON, and
// a relative server URL — none of which was written to suit this generator.
var realSpecs = []string{"petstore-3.0.yaml", "petstore-3.1.json"}

func TestRealSpecsGenerateRunnableCollections(t *testing.T) {
	for _, name := range realSpecs {
		t.Run(name, func(t *testing.T) {
			doc := mustLoad(t, filepath.Join("testdata", name))
			c, files, notes := Generate(doc)
			require.NotEmpty(t, files, "a real spec must produce requests")

			dir := t.TempDir()
			rep, err := Write(dir, files, c, newLock(name, "sha256:fixed"), false)
			require.NoError(t, err)
			require.Len(t, rep.Written, len(files)+1, "every file, plus collection.yaml")

			// Every emitted file has to load through the loader qv run uses.
			for _, f := range files {
				p := filepath.Join(dir, filepath.FromSlash(f.Path))
				data, rErr := os.ReadFile(p)
				require.NoError(t, rErr)

				parsed, pErr := request.Parse(data)
				require.NoError(t, pErr, "%s:\n%s", f.Path, data)
				require.NoError(t, parsed.Validate(), "%s:\n%s", f.Path, data)

				require.NotEmpty(t, parsed.Assertions, "%s asserts nothing", f.Path)
				require.True(t, strings.HasPrefix(parsed.HTTP.URL, "{{base}}"),
					"%s: every URL hangs off the collection's base", f.Path)
			}

			// Nothing in a real spec may produce a path that escapes the output
			// directory, however the spec spells its tags and operation ids.
			for _, f := range files {
				require.False(t, strings.Contains(f.Path, ".."), f.Path)
				require.False(t, filepath.IsAbs(f.Path), f.Path)
			}

			// A second generation over the same tree must change nothing.
			lock, err := LoadLock(dir)
			require.NoError(t, err)
			again, err := Write(dir, files, c, lock, false)
			require.NoError(t, err)
			require.False(t, again.Changed(), "a real spec must generate idempotently")

			t.Logf("%s: %d requests, %d notes", name, len(files), len(notes))
		})
	}
}

// The oauth2-only Petstore is the case that most needs saying out loud: quiver
// cannot perform the flow, and a collection that silently could not authenticate
// would look like a quiver bug rather than a missing feature.
func TestRealSpecReportsWhatItCouldNotExpress(t *testing.T) {
	doc := mustLoad(t, filepath.Join("testdata", "petstore-3.0.yaml"))
	_, _, notes := Generate(doc)

	joined := strings.Join(notes, "\n")
	require.Contains(t, joined, "oauth2")
	require.Contains(t, joined, "application/octet-stream",
		"a body media type that was not generated must be reported, not dropped")
}
