package gen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"
	"go.yaml.in/yaml/v4"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// jsonMediaType is the only request/response body kind generated. form and
// multipart are named as deferred in the spec rather than half-supported.
const jsonMediaType = "application/json"

// maxSkeletonDepth caps schemaSkeleton's recursion. A self-referential schema
// (Pet.friend -> Pet) is ordinary, not exotic, and resolving it eagerly would
// not terminate.
const maxSkeletonDepth = 5

// mapOperation turns one operation into a request file's content, plus any
// notes for the generation report.
//
// It takes the whole `security` index rather than the plan's bare
// scheme-name -> profile-name map because it needs two different things from
// it: which auth profile to reference, and which header names that profile will
// supply — emitting `Authorization` as a header parameter *and* attaching an
// auth profile puts two conflicting credentials on one request.
func mapOperation(ref operationRef, sec security) (request.Request, []string) {
	op := ref.op
	var notes []string
	where := fmt.Sprintf("%s %s", ref.method, ref.path)

	params, paramNotes := mergeParams(ref.pathParams, op.Parameters)
	notes = append(notes, prefixed(where, paramNotes)...)

	spec := &request.HTTPSpec{
		Method: ref.method,
		URL:    mapURL(ref.path),
	}

	auth, authNotes := mapAuth(op, sec)
	notes = append(notes, prefixed(where, authNotes)...)

	spec.Query = mapQuery(params)
	spec.Headers = mapHeaders(params, sec, auth != "")

	body, bodyNotes := mapBody(op)
	notes = append(notes, prefixed(where, bodyNotes)...)
	if body != "" {
		spec.Body = body
		if _, ok := spec.Headers["Content-Type"]; !ok {
			setHeader(&spec.Headers, "Content-Type", jsonMediaType)
		}
	}
	if acceptsJSON(op) {
		if _, ok := spec.Headers["Accept"]; !ok {
			setHeader(&spec.Headers, "Accept", jsonMediaType)
		}
	}

	assertion, assertNotes := mapAssertion(op)
	notes = append(notes, prefixed(where, assertNotes)...)

	return request.Request{
		Name:     operationName(ref),
		Protocol: request.ProtocolHTTP,
		HTTP:     spec,
		Auth:     auth,
		// timeout is deliberately not emitted: the collection default applies,
		// and a per-operation timeout invented from a spec would be a guess.
		Assertions: []request.Assertion{assertion},
	}, notes
}

// operationName is what the file's `name:` says, and therefore what the run
// report calls this request: the operationId, then the summary, then the
// method and path — always something a human can find in the spec.
func operationName(ref operationRef) string {
	if id := ref.op.OperationId; id != "" {
		return id
	}
	if s := ref.op.Summary; s != "" {
		return s
	}
	return ref.method + " " + ref.path
}

// pathParamPattern matches an OpenAPI path template segment, `{petId}`.
var pathParamPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// mapURL rewrites the spec's path template into quiver's own, under {{base}}.
//
// This is the single most important mapping in the package: it is what makes a
// generated file runnable with `-V petId=1` rather than a template someone has
// to edit before it does anything.
func mapURL(opPath string) string {
	return "{{base}}" + pathParamPattern.ReplaceAllStringFunc(opPath, func(m string) string {
		return "{{" + varName(pathParamPattern.FindStringSubmatch(m)[1]) + "}}"
	})
}

// varNameInvalid matches everything quiver's {{var}} syntax does not accept.
// A parameter named `pet id` would otherwise emit a template nothing can
// resolve and every run would fail with an unresolved-variable error.
var varNameInvalid = regexp.MustCompile(`[^\w.-]+`)

func varName(s string) string {
	return strings.Trim(varNameInvalid.ReplaceAllString(s, "_"), "_")
}

// mergeParams combines path-level parameters with the operation's own. An
// operation-level parameter with the same name and location overrides the
// path-level one, which is what the OpenAPI specification requires.
func mergeParams(pathLevel, opLevel []*v3high.Parameter) ([]*v3high.Parameter, []string) {
	var notes []string
	out := make([]*v3high.Parameter, 0, len(pathLevel)+len(opLevel))
	index := map[string]int{}

	add := func(p *v3high.Parameter) {
		if p == nil || p.Name == "" {
			return
		}
		key := p.In + ":" + p.Name
		if i, ok := index[key]; ok {
			out[i] = p
			return
		}
		index[key] = len(out)
		out = append(out, p)
	}
	for _, p := range pathLevel {
		add(p)
	}
	for _, p := range opLevel {
		add(p)
	}
	return out, notes
}

// mapQuery emits the query parameters worth writing down: the required ones,
// and the optional ones the spec gave a value for. Emitting every optional
// parameter would bury the two that actually matter.
func mapQuery(params []*v3high.Parameter) map[string]string {
	var out map[string]string
	for _, p := range params {
		if p.In != "query" {
			continue
		}
		if v, ok := paramValue(p); ok {
			setHeader(&out, p.Name, v)
			continue
		}
		if isRequired(p) {
			setHeader(&out, p.Name, "{{"+varName(p.Name)+"}}")
		}
	}
	return out
}

// mapHeaders is mapQuery for header parameters, minus any header the request's
// auth profile will supply itself.
func mapHeaders(params []*v3high.Parameter, sec security, authed bool) map[string]string {
	var out map[string]string
	for _, p := range params {
		if p.In != "header" {
			continue
		}
		if authed && sec.ownsHeader(p.Name) {
			continue // the auth profile supplies it; emitting both is a conflict
		}
		if v, ok := paramValue(p); ok {
			setHeader(&out, p.Name, v)
			continue
		}
		if isRequired(p) {
			setHeader(&out, p.Name, "{{"+varName(p.Name)+"}}")
		}
	}
	return out
}

// setHeader writes into a map that may not exist yet, so the zero value of a
// request with no headers marshals as an absent key rather than `{}`.
func setHeader(m *map[string]string, k, v string) {
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[k] = v
}

func isRequired(p *v3high.Parameter) bool { return p.Required != nil && *p.Required }

// paramValue reports the value the spec itself suggests for a parameter:
// its example, the first of its examples, or its schema's default or example.
func paramValue(p *v3high.Parameter) (string, bool) {
	if s, ok := nodeScalar(p.Example); ok {
		return s, true
	}
	if p.Examples != nil {
		for _, ex := range p.Examples.FromOldest() {
			if ex == nil {
				continue
			}
			if s, ok := nodeScalar(ex.Value); ok {
				return s, true
			}
		}
	}
	if p.Schema == nil {
		return "", false
	}
	schema := p.Schema.Schema()
	if schema == nil {
		return "", false
	}
	if s, ok := nodeScalar(schema.Default); ok {
		return s, true
	}
	if s, ok := nodeScalar(schema.Example); ok {
		return s, true
	}
	for _, n := range schema.Examples {
		if s, ok := nodeScalar(n); ok {
			return s, true
		}
	}
	return "", false
}

// nodeScalar renders a YAML node as the string a query or header value needs.
// Only scalars qualify: a mapping or sequence parameter needs `style` and
// `explode` handling that this generator does not do, and guessing would emit
// a value the API rejects.
func nodeScalar(n *yaml.Node) (string, bool) {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return "", false
	}
	return n.Value, true
}

// mapBody produces the request body: the declared example, the first named
// example, or a skeleton derived from the schema.
func mapBody(op *v3high.Operation) (string, []string) {
	if op.RequestBody == nil || op.RequestBody.Content == nil {
		return "", nil
	}
	mt := op.RequestBody.Content.GetOrZero(jsonMediaType)
	if mt == nil {
		var kinds []string
		for k := range op.RequestBody.Content.KeysFromOldest() {
			kinds = append(kinds, k)
		}
		if len(kinds) == 0 {
			return "", nil
		}
		return "", []string{fmt.Sprintf(
			"request body is %s, and only %s is generated; the body was left empty",
			strings.Join(kinds, ", "), jsonMediaType)}
	}

	if v, ok := nodeValue(mt.Example); ok {
		return encodeJSON(v)
	}
	if mt.Examples != nil {
		for _, ex := range mt.Examples.FromOldest() {
			if ex == nil {
				continue
			}
			if v, ok := nodeValue(ex.Value); ok {
				return encodeJSON(v)
			}
		}
	}
	if mt.Schema == nil {
		return "", nil
	}
	skeleton, truncated := schemaSkeleton(mt.Schema.Schema(), 0)
	if skeleton == nil {
		return "", nil
	}
	body, notes := encodeJSON(skeleton)
	if truncated {
		notes = append(notes, "request body schema is recursive; the skeleton was cut short")
	}
	return body, notes
}

// nodeValue decodes any YAML node into a Go value, for a body example that is
// a whole object rather than a scalar.
func nodeValue(n *yaml.Node) (any, bool) {
	if n == nil || n.Tag == "!!null" {
		return nil, false
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, false
	}
	return v, v != nil
}

func encodeJSON(v any) (string, []string) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", []string{fmt.Sprintf("request body example could not be encoded as JSON: %v", err)}
	}
	return string(out), nil
}

// schemaSkeleton builds the smallest body the API would accept: required
// properties only, with a type-appropriate zero value. Optional properties are
// left out on purpose — a skeleton carrying every field buries the two the API
// insists on.
//
// It reports whether recursion was cut short, so the report can say so rather
// than letting a silently truncated body look complete.
func schemaSkeleton(s *base.Schema, depth int) (any, bool) {
	if s == nil {
		return nil, false
	}
	if depth > maxSkeletonDepth {
		return map[string]any{}, true
	}

	// A composed schema has no type of its own. allOf is the composition that
	// can be merged; oneOf/anyOf are a choice, and the first branch is the only
	// defensible pick.
	if len(s.AllOf) > 0 {
		merged := map[string]any{}
		truncated := false
		for _, proxy := range s.AllOf {
			if proxy == nil {
				continue
			}
			part, cut := schemaSkeleton(proxy.Schema(), depth)
			truncated = truncated || cut
			if m, ok := part.(map[string]any); ok {
				for k, v := range m {
					merged[k] = v
				}
			}
		}
		return merged, truncated
	}
	for _, branch := range [][]*base.SchemaProxy{s.OneOf, s.AnyOf} {
		if len(branch) > 0 && branch[0] != nil {
			return schemaSkeleton(branch[0].Schema(), depth)
		}
	}

	switch schemaType(s) {
	case "object":
		out := map[string]any{}
		truncated := false
		for _, name := range s.Required {
			var prop *base.SchemaProxy
			if s.Properties != nil {
				prop = s.Properties.GetOrZero(name)
			}
			if prop == nil {
				out[name] = nil // required but undescribed: the key still belongs
				continue
			}
			v, cut := schemaSkeleton(prop.Schema(), depth+1)
			truncated = truncated || cut
			out[name] = v
		}
		return out, truncated
	case "array":
		return []any{}, false
	case "string":
		return "", false
	case "integer", "number":
		return 0, false
	case "boolean":
		return false, false
	default:
		return map[string]any{}, false
	}
}

// schemaType picks the type to generate for. 3.1 allows a list, typically
// ["string", "null"]; the null is a nullability statement, not a shape.
func schemaType(s *base.Schema) string {
	for _, t := range s.Type {
		if t != "null" {
			return t
		}
	}
	if s.Properties != nil && s.Properties.Len() > 0 {
		return "object" // untyped but with properties: every real spec means object
	}
	return ""
}

// acceptsJSON reports whether any declared response is JSON, which is what
// makes an `Accept: application/json` header honest rather than decorative.
func acceptsJSON(op *v3high.Operation) bool {
	if op.Responses == nil || op.Responses.Codes == nil {
		return false
	}
	for _, resp := range op.Responses.Codes.FromOldest() {
		if resp != nil && resp.Content != nil && resp.Content.GetOrZero(jsonMediaType) != nil {
			return true
		}
	}
	return false
}

// underFourHundred asserts "the request succeeded" without naming a status.
//
// The spec asks for `op: lt, value: "400"` here, but quiver's assertion
// vocabulary has no `lt` — adding one would be a schema change, which this
// plan's own constraints forbid, and emitting it anyway would produce a file
// `qv run` refuses to load. `matches` is in the vocabulary, works on `status`,
// and says the same thing.
const underFourHundred = `^[1-3][0-9]{2}$`

// mapAssertion gives every generated request something to assert: the lowest
// declared 2xx status. A request file that asserts nothing is a request file
// CI cannot fail on, which is most of quiver's point.
func mapAssertion(op *v3high.Operation) (request.Assertion, []string) {
	best := ""
	if op.Responses != nil && op.Responses.Codes != nil {
		for code := range op.Responses.Codes.KeysFromOldest() {
			n, err := strconv.Atoi(code)
			if err != nil || n < 200 || n > 299 {
				continue
			}
			if best == "" || code < best {
				best = code
			}
		}
	}
	if best != "" {
		return request.Assertion{
			Name: "ok", From: "status", Op: "eq", Value: request.Val(best),
		}, nil
	}
	return request.Assertion{
		Name: "ok", From: "status", Op: "matches", Value: request.Val(underFourHundred),
	}, []string{
		"declares no 2xx response; asserting the status is under 400 instead",
	}
}

// mapAuth resolves which auth profile this operation should reference.
//
// `security: []` on an operation is the one case where an empty array means
// something: the operation is explicitly public, and overrides the document's
// own security. libopenapi preserves the distinction, so this does too.
func mapAuth(op *v3high.Operation, sec security) (string, []string) {
	reqs := sec.defaults
	if op.Security != nil {
		reqs = op.Security
	}
	var unknown []string
	for _, r := range reqs {
		if r == nil || r.Requirements == nil {
			continue
		}
		for name := range r.Requirements.KeysFromOldest() {
			if profile, ok := sec.profiles[name]; ok {
				return profile, nil // first usable scheme wins; alternatives are a choice
			}
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return "", []string{fmt.Sprintf(
			"requires security scheme %s, which has no generated auth profile; the request is unauthenticated",
			strings.Join(quoteAll(unknown), " or "))}
	}
	return "", nil
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strconv.Quote(s)
	}
	return out
}

// prefixed labels each note with the operation it came from, so a report over a
// 200-endpoint spec says which endpoint it is talking about.
func prefixed(where string, notes []string) []string {
	if len(notes) == 0 {
		return nil
	}
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = where + ": " + n
	}
	return out
}
