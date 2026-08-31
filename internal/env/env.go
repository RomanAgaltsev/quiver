// Package env loads environments, merges and resolves variables (including
// {{env:NAME}} secret references), expands {{templates}} and resolves a
// Request into a core.ResolvedRequest.
package env

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/secret"
)

// varPattern matches {{name}} and {{name | filter}}.
var varPattern = regexp.MustCompile(`\{\{\s*([\w.-]+)\s*(?:\|\s*([\w]+)\s*)?\}\}`)

// secretPattern matches a secret reference: {{env:NAME}}.
var secretPattern = regexp.MustCompile(`\{\{\s*env:([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// leftoverPattern matches any {{ … }} that survived expansion. Anything still
// looking like a template afterwards is a definition the user got wrong, and
// sending it verbatim — as an Authorization header, say — is never what they
// meant. The inner class excludes braces so brace-heavy content that is not a
// template (a GraphQL selection set, a JSON body) cannot false-positive.
var leftoverPattern = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// Resolved is the merged variable set plus the concrete secret values that must
// never be printed or persisted.
type Resolved struct {
	Vars    map[string]string
	Secrets []string
	// Redactor, when set, is kept in step with Secrets. Secrets are not all known
	// up front: a {{env:NAME}} written inside a request file is only discovered
	// during resolution, after render and history were handed their redactor.
	// Leaving it nil (as --show-secrets does) simply redacts nothing.
	Redactor *secret.Redactor
}

// MergeVars applies the documented precedence - collection defaults < selected
// environment < --var overrides - then resolves every {{env:NAME}}
// secret reference from the process environment.
//
// Every value resolved from the environment is collected into Secrets so that
// render, dry-run and history can redact it.
func MergeVars(defaults, envVars, overrides map[string]string) (*Resolved, error) {
	vars := make(map[string]string, len(defaults)+len(envVars)+len(overrides))
	for _, layer := range []map[string]string{defaults, envVars, overrides} {
		for k, v := range layer {
			vars[k] = v
		}
	}

	res := &Resolved{Vars: vars}
	var missing []string
	for k, v := range vars {
		vars[k] = secretPattern.ReplaceAllStringFunc(v, func(m string) string {
			name := secretPattern.FindStringSubmatch(m)[1]
			val, ok := os.LookupEnv(name)
			if !ok {
				missing = append(missing, fmt.Sprintf("%s (referenced by variable %q)", name, k))
				return m
			}
			res.AddSecret(val)
			return val
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, core.NewConfigError(
			fmt.Errorf("unset environment variable(s): %s", strings.Join(missing, ", ")))
	}
	return res, nil
}

// AddSecret records a concrete secret value for redaction, and propagates it to
// the attached Redactor so output and history written *after this moment* are
// covered. Empty values are dropped: replacing "" would corrupt every string it
// touched. The set is kept longest-first so redacting never leaves the tail of a
// longer secret behind.
func (res *Resolved) AddSecret(v string) {
	if v == "" || slices.Contains(res.Secrets, v) {
		return
	}
	res.Secrets = append(res.Secrets, v)
	sort.SliceStable(res.Secrets, func(i, j int) bool {
		return len(res.Secrets[i]) > len(res.Secrets[j])
	})
	res.Redactor.Add(v)
}

// Expand substitutes {{var}} occurrences and resolves {{env:NAME}} secret
// references, and rejects anything that still looks like a template afterwards.
// Unknown variables, unset secret references and leftovers are all config errors.
//
// It is a method rather than a free function so that a secret resolved *inside a
// request file* joins res.Secrets and is therefore redacted by render, --dry-run
// and history. A resolved-but-unredacted token would be strictly worse than an
// unresolved one.
//
// The optional `| json` filter JSON-escapes the value. It is required whenever a
// captured value is interpolated into a JSON body, message or variables block:
// raw substitution of a value containing a quote, backslash or newline produces
// invalid JSON.
func (res *Resolved) Expand(s string) (string, error) {
	if s == "" {
		return s, nil
	}
	var problems []string

	// Secret references first. They are lexically distinct and must work in a
	// request file, not only in a variable map: varPattern's name class excludes
	// the colon, so {{env:NAME}} written inline used to match nothing at all and
	// was neither expanded nor reported.
	out := secretPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := secretPattern.FindStringSubmatch(m)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			problems = append(problems, fmt.Sprintf("unset environment variable %q", name))
			return m
		}
		res.AddSecret(v)
		return v
	})

	out = varPattern.ReplaceAllStringFunc(out, func(m string) string {
		g := varPattern.FindStringSubmatch(m)
		name, filter := g[1], g[2]
		v, ok := res.Vars[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("unresolved variable %q", name))
			return m
		}
		switch filter {
		case "":
			return v
		case "json":
			b, err := json.Marshal(v)
			if err != nil { // unreachable for a string, but never swallow it
				problems = append(problems, fmt.Sprintf("json filter on %q: %v", name, err))
				return m
			}
			return string(b)
		default:
			problems = append(problems, fmt.Sprintf("unknown filter %q on %q (only |json is supported)", filter, name))
			return m
		}
	})

	if len(problems) == 0 {
		if left := leftoverPattern.FindString(out); left != "" {
			problems = append(problems, fmt.Sprintf(
				"unexpanded template %s (a variable value cannot itself contain a template)", left))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return "", core.NewConfigError(fmt.Errorf("%s", strings.Join(problems, "; ")))
	}
	return out, nil
}

// ExpandMap expands every value in a map. Header, query and metadata values are
// as much a part of a request as its URL is, so they go through the same path.
func (res *Resolved) ExpandMap(in map[string]string) (map[string]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		ev, err := res.Expand(v)
		if err != nil {
			return nil, err
		}
		out[k] = ev
	}
	return out, nil
}

// ExpandAll expands a set of string pointers in place, stopping at the first
// error. It is what the ad-hoc commands use for their positional arguments.
func (res *Resolved) ExpandAll(values ...*string) error {
	for _, p := range values {
		if p == nil || *p == "" {
			continue
		}
		v, err := res.Expand(*p)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
}

// LoadEnvironment reads a YAML map of variables from a file.
func LoadEnvironment(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, core.NewConfigError(fmt.Errorf("read environment: %w", err))
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, core.NewConfigError(fmt.Errorf("parse environment: %w", err))
	}
	return m, nil
}

// Resolve expands every template in r, loads any body_file and resolves its
// auth profile. Every failure here is the definition's fault, so all of them are
// config errors (exit 2), not run failures.
func Resolve(r *request.Request, res *Resolved, auth map[string]request.AuthProfile) (*core.ResolvedRequest, error) {
	rr := &core.ResolvedRequest{
		Name:     r.Name,
		Protocol: r.Protocol,
		Timeout:  r.Timeout.Duration(),
	}
	fail := func(format string, args ...any) error {
		return core.NewConfigError(fmt.Errorf(format, args...))
	}
	var err error
	switch r.Protocol {
	case request.ProtocolHTTP:
		spec := *r.HTTP
		// body_file is read relative to the request file, then expanded exactly
		// like an inline body. Validate() has already rejected setting both.
		if spec.BodyFile != "" {
			path := spec.BodyFile
			if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(r.Path), path)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, fail("resolve %q: body_file: %w", r.Name, readErr)
			}
			spec.Body = string(raw)
			spec.BodyFile = ""
		}
		if spec.URL, err = res.Expand(spec.URL); err != nil {
			return nil, fail("resolve %q: http.url: %w", r.Name, err)
		}
		if spec.Body, err = res.Expand(spec.Body); err != nil {
			return nil, fail("resolve %q: http.body: %w", r.Name, err)
		}
		if spec.Headers, err = res.ExpandMap(spec.Headers); err != nil {
			return nil, fail("resolve %q: http.headers: %w", r.Name, err)
		}
		if spec.Query, err = res.ExpandMap(spec.Query); err != nil {
			return nil, fail("resolve %q: http.query: %w", r.Name, err)
		}
		rr.HTTP = &spec
	case request.ProtocolGRPC:
		spec := *r.GRPC
		if spec.Target, err = res.Expand(spec.Target); err != nil {
			return nil, fail("resolve %q: grpc.target: %w", r.Name, err)
		}
		if spec.Message, err = res.Expand(spec.Message); err != nil {
			return nil, fail("resolve %q: grpc.message: %w", r.Name, err)
		}
		if spec.Metadata, err = res.ExpandMap(spec.Metadata); err != nil {
			return nil, fail("resolve %q: grpc.metadata: %w", r.Name, err)
		}
		// proto_files are paths, resolved relative to the request file (Q26).
		// Copied, not rewritten in place: a shallow struct copy shares the slice's
		// backing array, so writing through it would mutate the caller's Request
		// and make the loaded object differ from its file.
		if len(spec.ProtoFiles) > 0 {
			files := make([]string, len(spec.ProtoFiles))
			for i, p := range spec.ProtoFiles {
				if filepath.IsAbs(p) {
					files[i] = p
					continue
				}
				files[i] = filepath.Join(filepath.Dir(r.Path), p)
			}
			spec.ProtoFiles = files
		}
		rr.GRPC = &spec
	case request.ProtocolGraphQL:
		spec := *r.GraphQL
		if spec.URL, err = res.Expand(spec.URL); err != nil {
			return nil, fail("resolve %q: graphql.url: %w", r.Name, err)
		}
		if spec.Query, err = res.Expand(spec.Query); err != nil {
			return nil, fail("resolve %q: graphql.query: %w", r.Name, err)
		}
		if spec.Variables, err = res.Expand(spec.Variables); err != nil {
			return nil, fail("resolve %q: graphql.variables: %w", r.Name, err)
		}
		if spec.Headers, err = res.ExpandMap(spec.Headers); err != nil {
			return nil, fail("resolve %q: graphql.headers: %w", r.Name, err)
		}
		rr.GraphQL = &spec
	default:
		return nil, fail("resolve: unknown protocol %q", r.Protocol)
	}
	if r.Auth != "" {
		prof, ok := auth[r.Auth]
		if !ok {
			return nil, fail("resolve %q: auth profile %q not found in collection.yaml", r.Name, r.Auth)
		}
		for _, p := range []*string{&prof.Username, &prof.Password, &prof.Token, &prof.Key} {
			if *p, err = res.Expand(*p); err != nil {
				return nil, fail("resolve %q: auth %q: %w", r.Name, r.Auth, err)
			}
		}
		rr.Auth = &prof
	}
	return rr, nil
}
