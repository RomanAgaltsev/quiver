// Package gen turns an API description into a runnable quiver collection.
//
// It is deliberately split into a pure half and an I/O half: LoadDocument,
// Operations and Generate produce values and never touch the filesystem, and
// Write is the only thing that does. That is what makes the mapping testable
// without a temp directory, and the lockfile decisions testable without a spec.
package gen

import (
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// LoadDocument parses an OpenAPI 3.x document and returns its high-level model
// with references resolved.
//
// A 2.0 document is rejected by name rather than half-parsed: libopenapi can
// read it, but the mapping differs enough that emitting a collection from one
// would produce quietly wrong output.
func LoadDocument(data []byte) (*v3high.Document, error) {
	doc, err := libopenapi.NewDocumentWithConfiguration(data, &datamodel.DocumentConfiguration{
		// A self-referential schema — Pet.friend: Pet, Comment.replies: [Comment] —
		// is ordinary in real specs, and libopenapi treats one as a fatal build
		// error by default. Refusing to generate anything from such a document
		// would rule out most large APIs; schemaSkeleton caps its own recursion
		// instead, which is where the problem actually has to be solved.
		SkipCircularReferenceCheck: true,
		// Reading a spec must not fetch anything. Both default to false; saying
		// so here keeps `qv gen` from quietly gaining network access if the
		// library's defaults ever change.
		AllowFileReferences:   false,
		AllowRemoteReferences: false,
		// libopenapi logs resolution problems through slog. Its default handler
		// writes JSON lines to stderr, which would interleave with the CLI's own
		// report; the errors it cares about are returned, not only logged.
		Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	if v := doc.GetVersion(); strings.HasPrefix(v, "2.") {
		return nil, fmt.Errorf(
			"OpenAPI %s (Swagger 2.0) is not supported; convert it to 3.x first", v)
	}

	model, err := doc.BuildV3Model()
	if err != nil {
		return nil, fmt.Errorf("build model: %w", err)
	}
	return &model.Model, nil
}

// GeneratedFile is one request file the generator would write: where it goes
// relative to the output directory, and what goes in it.
type GeneratedFile struct {
	Path        string
	Req         request.Request
	OperationID string
}

// operationRef is one operation together with the context its mapping needs:
// the method and path it hangs under, and the path-level parameters every
// operation on that path inherits.
type operationRef struct {
	method     string
	path       string
	op         *v3high.Operation
	pathParams []*v3high.Parameter
}

// Operations enumerates every operation in the document, in spec order.
//
// Order is the document's own insertion order rather than sorted: it is the
// order the spec's author chose, and it makes the generation report read like
// the file the user is looking at.
func Operations(doc *v3high.Document) []operationRef {
	if doc == nil || doc.Paths == nil || doc.Paths.PathItems == nil {
		return nil
	}
	var refs []operationRef
	for p, item := range doc.Paths.PathItems.FromOldest() {
		if item == nil {
			continue
		}
		ops := item.GetOperations()
		if ops == nil {
			continue
		}
		for method, op := range ops.FromOldest() {
			if op == nil {
				continue
			}
			refs = append(refs, operationRef{
				method:     strings.ToUpper(method),
				path:       p,
				op:         op,
				pathParams: item.Parameters,
			})
		}
	}
	return refs
}

// filePath is where one operation's request file lives, relative to the output
// directory: `<tag>/<operationId>.yaml`, falling back to `<method>-<path>` when
// the operation has no operationId, and to the output root when it has no tags.
//
// Grouping by tag is not an invention: it is the grouping the spec's own author
// already chose, and every OpenAPI UI renders it.
func filePath(method, opPath, operationID string, tags []string) string {
	name := sanitize(operationID)
	if name == "" {
		name = strings.ToLower(method) + "-" + sanitize(opPath)
		name = strings.Trim(name, "-")
	}

	var dir string
	if len(tags) > 0 {
		dir = slugify(tags[0]) // first tag only; an operation lives in one place
	}
	return path.Join(dir, name+".yaml")
}

// sanitize reduces a spec-supplied string to a single safe path segment,
// preserving case: an operationId is a name the user recognises, and
// lower-casing `getPetById` into `getpetbyid` would make the tree unreadable.
//
// Every run of anything outside [A-Za-z0-9] collapses to one "-", which is what
// makes `..` and any embedded separator impossible rather than merely unlikely.
func sanitize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// slugify is sanitize plus lower-casing, for a tag becoming a directory name.
// A tag of "///" has nothing left after slugification and returns "", which the
// caller must read as "no directory" rather than as a path.
func slugify(s string) string { return strings.ToLower(sanitize(s)) }

// Generate maps a whole document: the collection file, one GeneratedFile per
// operation, and the notes that become the generation report.
//
// It is pure. Nothing here reads or writes the filesystem, which is why the
// golden tests need no temp directory and --check can run the whole thing in
// memory before deciding whether anything would change.
func Generate(doc *v3high.Document) (collectionFile, []GeneratedFile, []string) {
	coll, sec, notes := mapCollection(doc)

	var files []GeneratedFile
	seen := map[string]int{}
	for _, ref := range Operations(doc) {
		req, opNotes := mapOperation(ref, sec)
		notes = append(notes, opNotes...)

		p := filePath(ref.method, ref.path, ref.op.OperationId, ref.op.Tags)
		// Two operations can slugify onto the same path — `getPet` and `get-pet`,
		// or two untagged operations differing only in punctuation. Overwriting
		// one with the other would drop an endpoint silently.
		if n := seen[p]; n > 0 {
			ext := path.Ext(p)
			p = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(p, ext), n+1, ext)
			notes = append(notes, fmt.Sprintf(
				"%s %s: file name collided with an earlier operation; written as %s",
				ref.method, ref.path, p))
		}
		seen[filePath(ref.method, ref.path, ref.op.OperationId, ref.op.Tags)]++

		files = append(files, GeneratedFile{Path: p, Req: req, OperationID: ref.op.OperationId})
	}
	return coll, files, notes
}
