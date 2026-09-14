package gen

import (
	"os"
	"testing"

	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// mustLoad reads a fixture and builds its model, failing the test if it cannot.
func mustLoad(t *testing.T, path string) *v3high.Document {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	doc, err := LoadDocument(data)
	require.NoError(t, err)
	return doc
}

// mustMapCollection maps a fixture's collection-level output.
func mustMapCollection(t *testing.T, path string) (collectionFile, security, []string) {
	t.Helper()
	return mapCollection(mustLoad(t, path))
}

// mapOne maps one operation of a fixture, named by method and path, and returns
// its notes as well. The security index comes from the same document, because
// an operation's mapping depends on it — a header an auth profile owns is not
// emitted twice.
func mapOne(t *testing.T, path, method, opPath string) (request.Request, []string) {
	t.Helper()
	doc := mustLoad(t, path)
	_, sec, _ := mapCollection(doc)
	for _, ref := range Operations(doc) {
		if ref.method == method && ref.path == opPath {
			return mapOperation(ref, sec)
		}
	}
	t.Fatalf("fixture %s has no %s %s", path, method, opPath)
	return request.Request{}, nil
}

// mustMap is mapOne when the test only cares about the request.
func mustMap(t *testing.T, path, method, opPath string) request.Request {
	t.Helper()
	req, _ := mapOne(t, path, method, opPath)
	return req
}
