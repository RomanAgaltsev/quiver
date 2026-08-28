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
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// varPattern matches {{name}} and {{name | filter}}.
var varPattern = regexp.MustCompile(`\{\{\s*([\w.-]+)\s*(?:\|\s*([\w]+)\s*)?\}\}`)

// secretPattern matches a secret reference: {{env:NAME}}.
var secretPattern = regexp.MustCompile(`\{\{\s*env:([A-Za-z_][A-Za-z0-9_]*)\s*\}\}`)

// Resolved is the merged variable set plus the concrete secret values that must
// never be printed or persisted.
type Resolved struct {
	Vars    map[string]string
	Secrets []string
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

	secrets := make(map[string]struct{})
	var missing []string
	for k, v := range vars {
		vars[k] = secretPattern.ReplaceAllStringFunc(v, func(m string) string {
			name := secretPattern.FindStringSubmatch(m)[1]
			val, ok := os.LookupEnv(name)
			if !ok {
				missing = append(missing, fmt.Sprintf("%s (referenced by variable %q)", name, k))
				return m
			}
			if val != "" {
				secrets[val] = struct{}{}
			}
			return val
		})
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("unset environment variable(s): %s", strings.Join(missing, ", "))
	}

	res := &Resolved{
		Vars:    vars,
		Secrets: make([]string, 0, len(secrets)),
	}
	for s := range secrets {
		res.Secrets = append(res.Secrets, s)
	}

	// Longest first, so redacting never leaves a fragment of a longer secret behind.
	sort.Slice(res.Secrets, func(i, j int) bool {
		return len(res.Secrets[i]) > len(res.Secrets[j])
	})

	return res, nil
}

// Expand substitutes {{var}} occurrences. Unknown variables are an error.
//
// The optional `| json` filter JSON-escapes the value. It is required whenever a
// captured value is interpolated into a JSON body, message or variables block:
// raw substitution of a value containing a quote, backlash or newline produces
// invalid JSON.
func Expand(s string, vars map[string]string) (string, error) {
	var problems []string
	out := varPattern.ReplaceAllStringFunc(s, func(m string) string {
		g := varPattern.FindStringSubmatch(m)
		name, filter := g[1], g[2]
		v, ok := vars[name]
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
	if len(problems) > 0 {
		sort.Strings(problems)
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return out, nil
}

func expandMap(in map[string]string, vars map[string]string) (map[string]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		ev, err := Expand(v, vars)
		if err != nil {
			return nil, err
		}
		out[k] = ev
	}
	return out, nil
}

// LoadEnvironment reads a YAML map of variables from a file.
func LoadEnvironment(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read environment: %w", err)
	}
	var m map[string]string
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse environment: %w", err)
	}
	return m, nil
}

// Resolve expands every template in r, loads any body_file and resolves its
// auth profile.
func Resolve(r *request.Request, res *Resolved, auth map[string]request.AuthProfile) (*core.ResolvedRequest, error) {
	vars := res.Vars
	rr := &core.ResolvedRequest{
		Name:     r.Name,
		Protocol: r.Protocol,
		Timeout:  r.Timeout.Duration(),
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
				return nil, fmt.Errorf("resolve %q: body_file: %w", r.Name, readErr)
			}
			spec.Body = string(raw)
			spec.BodyFile = ""
		}
		if spec.URL, err = Expand(spec.URL, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: http.url: %w", r.Name, err)
		}
		if spec.Body, err = Expand(spec.Body, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: http.body: %w", r.Name, err)
		}
		if spec.Headers, err = expandMap(spec.Headers, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: http.headers: %w", r.Name, err)
		}
		if spec.Query, err = expandMap(spec.Query, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: http.query: %w", r.Name, err)
		}
		rr.HTTP = &spec
	case request.ProtocolGRPC:
		spec := *r.GRPC
		if spec.Target, err = Expand(spec.Target, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: grpc.target: %w", r.Name, err)
		}
		if spec.Message, err = Expand(spec.Message, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: grpc.message: %w", r.Name, err)
		}
		if spec.Metadata, err = expandMap(spec.Metadata, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: grpc.metadata: %w", r.Name, err)
		}
		// proto_files are paths, resolved relative to the request file (Q26).
		for i, p := range spec.ProtoFiles {
			if !filepath.IsAbs(p) {
				spec.ProtoFiles[i] = filepath.Join(filepath.Dir(r.Path), p)
			}
		}
		rr.GRPC = &spec
	case request.ProtocolGraphQL:
		spec := *r.GraphQL
		if spec.URL, err = Expand(spec.URL, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: graphql.url: %w", r.Name, err)
		}
		if spec.Query, err = Expand(spec.Query, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: graphql.query: %w", r.Name, err)
		}
		if spec.Variables, err = Expand(spec.Variables, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: graphql.variables: %w", r.Name, err)
		}
		if spec.Headers, err = expandMap(spec.Headers, vars); err != nil {
			return nil, fmt.Errorf("resolve %q: graphql.headers: %w", r.Name, err)
		}
		rr.GraphQL = &spec
	default:
		return nil, fmt.Errorf("resolve: unknown protocol %q", r.Protocol)
	}
	if r.Auth != "" {
		prof, ok := auth[r.Auth]
		if !ok {
			return nil, fmt.Errorf("resolve %q: auth profile %q not found in collection.yaml", r.Name, r.Auth)
		}
		for _, p := range []*string{&prof.Username, &prof.Password, &prof.Token, &prof.Key} {
			if *p, err = Expand(*p, vars); err != nil {
				return nil, fmt.Errorf("resolve %q: auth %q: %w", r.Name, r.Auth, err)
			}
		}
		rr.Auth = &prof
	}
	return rr, nil
}
