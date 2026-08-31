// Package request defines the on-disk request model and its validation.
package request

import (
	"bytes"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"time"

	"github.com/goccy/go-yaml"
)

// Protocol selects the executor and the request spec to decode.
type Protocol string

// The three MVP protocols; ad-hoc commands and request files share them.
const (
	ProtocolHTTP    Protocol = "http"
	ProtocolGRPC    Protocol = "grpc"
	ProtocolGraphQL Protocol = "graphql"
)

// Duration is a YAML-friendly time.Duration ("5s", "1500ms")
type Duration struct {
	d time.Duration
}

// Duration exposes the parsed value of a Timeout field.
func (dur Duration) Duration() time.Duration { return dur.d }

// UnmarshalYAML uses goccy's InterfaceUnmarshaler form on purpose. The []byte
// form hands the hook re-serialized YAML *source* for the node: a plain scalar
// that is not the last line of the document arrives with its trailing newline,
// and a quoted one arrives with its quotes. Feeding that to time.ParseDuration
// means reimplementing scalar unquoting. Letting the decoder produce the string
// removes that whole class of bug.
func (dur *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	if s == "" {
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("timeout %q: %w", s, err)
	}
	dur.d = parsed
	return nil
}

// Request is one request file. Exactly one protocol spec must be set; Parse
// and Validate enforce the rest.
type Request struct {
	Name     string   `yaml:"name"`
	Protocol Protocol `yaml:"protocol"`
	// Order sequences a folder run. Request without an order sort last.
	Order      *int         `yaml:"order,omitempty"`
	Timeout    Duration     `yaml:"timeout,omitempty"`
	HTTP       *HTTPSpec    `yaml:"http,omitempty"`
	GRPC       *GRPCSpec    `yaml:"grpc,omitempty"`
	GraphQL    *GraphQLSpec `yaml:"graphql,omitempty"`
	Auth       string       `yaml:"auth,omitempty"`
	Captures   []Capture    `yaml:"captures,omitempty"`
	Assertions []Assertion  `yaml:"assertions,omitempty"`
	// Path is the file this request was loaded from. Not part of the file format.
	// Set by colection.LoadRequest and needed by history/replay.
	Path string `yaml:"-"`
}

// HTTPSpec is the http-protocol request body of a request file.
type HTTPSpec struct {
	Method  string            `yaml:"method"`
	URL     string            `yaml:"url"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Query   map[string]string `yaml:"query,omitempty"`
	Body    string            `yaml:"body,omitempty"`
	// BodyFile reads the body from a file resolved relative to the request file.
	// Keeping large payloads in their own .json is *more* git-friendly, not less.
	BodyFile string `yaml:"body_file,omitempty"`
}

// EffectiveURL returns the URL that will actually be requested: URL with any
// `query:` entries merged in. Both the executor and --dry-run call it, so the
// preview and the wire cannot disagree.
//
// A hand-written query string is left exactly as given when there is nothing to
// merge: url.Values.Encode sorts keys and re-percent-encodes, which silently
// breaks signed URLs and order-sensitive filter DSLs.
func (s *HTTPSpec) EffectiveURL() (string, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return "", fmt.Errorf("bad url %q: %w", s.URL, err)
	}
	if len(s.Query) == 0 {
		return u.String(), nil
	}
	q := u.Query()
	// Deterministic order: ranging the map directly would shuffle equal-key
	// ordering between runs, and --dry-run output has to be stable.
	keys := make([]string, 0, len(s.Query))
	for k := range s.Query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q.Add(k, s.Query[k]) // Add, not Set: `?tag=x` plus query:{tag: y} means both
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// GRPCSpec is the grpc-protocol request body of a request file: a unary call
// to target with a JSON-encoded message.
type GRPCSpec struct {
	Target     string            `yaml:"target"` // host:port
	Method     string            `yaml:"method"` // pkg.Service/Method
	Metadata   map[string]string `yaml:"metadata,omitempty"`
	Message    string            `yaml:"message,omitempty"`     // request message as JSON
	ProtoFiles []string          `yaml:"proto_files,omitempty"` // optional, reflection used if empty
	Plaintext  bool              `yaml:"plaintext,omitempty"`
}

// GraphQLSpec is the graphql-protocol request body of a request file: a
// query (and optional variables JSON) POSTed as application/json.
type GraphQLSpec struct {
	URL       string            `yaml:"url"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Query     string            `yaml:"query"`
	Variables string            `yaml:"variables,omitempty"` // JSON object
}

// Capture extracts a value from a response into a variable.
type Capture struct {
	Var  string `yaml:"var"`
	From string `yaml:"from"`           // "body" | "header" | "status"
	Path string `yaml:"path,omitempty"` // gjson path (body) or header name
}

// Assertion is a declarative check over a response.
//
// Value is a pointer so that an explicit `value: ""` — asserting a field *is*
// the empty string, which real APIs return — is distinguishable from omitting
// the key entirely. Requiring a non-empty operand made that assertion
// impossible to write.
type Assertion struct {
	Name  string  `yaml:"name,omitempty"`
	From  string  `yaml:"from"`           // "body" | "header" | "status"
	Path  string  `yaml:"path,omitempty"` // gjson path (body) or header name
	Op    string  `yaml:"op"`             // see validOps
	Value *string `yaml:"value,omitempty"`
}

// Val returns a pointer to v, for building an Assertion in Go rather than YAML.
func Val(v string) *string { return &v }

// Operand returns the assertion's comparison value, or "" when unset.
func (a Assertion) Operand() string {
	if a.Value == nil {
		return ""
	}
	return *a.Value
}

// validSources are the response locations a capture or assertion may read.
var validSources = map[string]bool{
	"body": true, "header": true, "status": true,
}

// validOps are the assertion operators. `not_exists` exists because asserting
// *absence* is required to check a GraphQL response for an `errors` key.
// `matches` is the common fifth operator and is free to add here.
var validOps = map[string]bool{
	"eq": true, "ne": true, "exists": true, "not_exists": true,
	"contains": true, "matches": true,
}

// opNeedsValue reports whether an operator compares against Value.
func opNeedsValue(op string) bool {
	return op == "eq" || op == "ne" || op == "contains" || op == "matches"
}

// AuthProfile is referenced by name from a request, defined in collection.yaml.
type AuthProfile struct {
	Type     string `yaml:"type"`               // "basic" | "bearer" | "apikey"
	Username string `yaml:"username,omitempty"` // basic
	Password string `yaml:"password,omitempty"` // basic (often a secret ref)
	Token    string `yaml:"token,omitempty"`    // bearer (often a secret ref)
	Header   string `yaml:"header,omitempty"`   // apikey header name, e.g. "X-API-Key"
	Key      string `yaml:"key,omitempty"`      // apikey value (often a secret ref)
}

// Validate rejects a profile no executor can act on. An apikey profile with no
// header name used to be a silent no-op: the request went out unauthenticated
// and the only symptom was a 401 that looked like a server problem.
func (p AuthProfile) Validate(name string) error {
	switch p.Type {
	case "basic", "bearer":
		return nil
	case "apikey":
		if p.Header == "" {
			return fmt.Errorf("auth profile %q: type apikey requires a header name", name)
		}
		return nil
	default:
		return fmt.Errorf("auth profile %q: unknown type %q (want basic, bearer, or apikey)", name, p.Type)
	}
}

// Parse decodes a request file. Unknown fields are rejected so a mistyped key is
// an error rather than a silently ignored setting (Q16). goccy/go-yaml reports the
// line and column and prints an annotated snippet, which is what makes the spec §8
// "clear message" promise real.
func Parse(data []byte) (*Request, error) {
	var r Request
	dec := yaml.NewDecoder(bytes.NewReader(data), yaml.DisallowUnknownField())
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	return &r, nil
}

// Validate checks everything that can be checked without sending a request, so a
// malformed file is a config error (exit 2) before anything hits the network.
func (r *Request) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("request: name is required")
	}
	if err := r.validateProtocolBlock(); err != nil {
		return err
	}
	if err := r.validateCaptures(); err != nil {
		return err
	}
	return r.validateAssertions()
}

func (r *Request) validateProtocolBlock() error {
	// A block belonging to another protocol is a copy-paste error, not a no-op.
	present := map[Protocol]bool{ProtocolHTTP: r.HTTP != nil, ProtocolGRPC: r.GRPC != nil, ProtocolGraphQL: r.GraphQL != nil}
	for proto, ok := range present {
		if ok && proto != r.Protocol {
			return fmt.Errorf("request %q: protocol is %q but a %q block is also present", r.Name, r.Protocol, proto)
		}
	}

	switch r.Protocol {
	case ProtocolHTTP:
		if r.HTTP == nil {
			return fmt.Errorf("request %q: protocol http requires an http block", r.Name)
		}
		if r.HTTP.Method == "" || r.HTTP.URL == "" {
			return fmt.Errorf("request %q: http.method and http.url are required", r.Name)
		}
		if r.HTTP.Body != "" && r.HTTP.BodyFile != "" {
			return fmt.Errorf("request %q: set http.body or http.body_file, not both", r.Name)
		}
	case ProtocolGRPC:
		if r.GRPC == nil {
			return fmt.Errorf("request %q: protocol grpc requires a grpc block", r.Name)
		}
		if r.GRPC.Target == "" || r.GRPC.Method == "" {
			return fmt.Errorf("request %q: grpc.target and grpc.method are required", r.Name)
		}
	case ProtocolGraphQL:
		if r.GraphQL == nil {
			return fmt.Errorf("request %q: protocol graphql requires a graphql block", r.Name)
		}
		if r.GraphQL.URL == "" || r.GraphQL.Query == "" {
			return fmt.Errorf("request %q: graphql.url and graphql.query are required", r.Name)
		}
	default:
		return fmt.Errorf("request %q: unknown protocol %q (want http, grpc, or graphql)", r.Name, r.Protocol)
	}
	return nil
}

func (r *Request) validateCaptures() error {
	for i, c := range r.Captures {
		where := fmt.Sprintf("request %q: captures[%d]", r.Name, i)
		if c.Var == "" {
			return fmt.Errorf("%s: var is required", where)
		}
		if !validSources[c.From] {
			return fmt.Errorf("%s: unknown source %q (want body, header, or status)", where, c.From)
		}
		if c.From != "status" && c.Path == "" {
			return fmt.Errorf("%s: path is required when capturing from %s", where, c.From)
		}
	}
	return nil
}

func (r *Request) validateAssertions() error {
	for i, a := range r.Assertions {
		where := fmt.Sprintf("request %q: assertions[%d]", r.Name, i)
		if a.Name != "" {
			where = fmt.Sprintf("request %q: assertion %q", r.Name, a.Name)
		}
		if !validSources[a.From] {
			return fmt.Errorf("%s: unknown source %q (want body, header, or status)", where, a.From)
		}
		if !validOps[a.Op] {
			return fmt.Errorf("%s: unknown op %q (want eq, ne, exists, not_exists, contains, or matches)", where, a.Op)
		}
		if a.From != "status" && a.Path == "" {
			return fmt.Errorf("%s: path is required when asserting on %s", where, a.From)
		}
		if opNeedsValue(a.Op) && a.Value == nil {
			return fmt.Errorf("%s: op %q requires a value (write `value: \"\"` to assert emptiness)", where, a.Op)
		}
		if a.Op == "matches" {
			if _, err := regexp.Compile(a.Operand()); err != nil {
				return fmt.Errorf("%s: invalid regex %q: %w", where, a.Operand(), err)
			}
		}
	}
	return nil
}
