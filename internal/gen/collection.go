package gen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3high "github.com/pb33f/libopenapi/datamodel/high/v3"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Collection is the generated collection.yaml.
//
// It is a narrow mirror of collection.Collection rather than that type itself:
// the generator has no business emitting `fail_on_error` or a collection-wide
// `timeout`, and marshalling the full struct would write both on every run.
type Collection struct {
	Defaults map[string]string              `yaml:"defaults"`
	Auth     map[string]request.AuthProfile `yaml:"auth,omitempty"`

	// comments carry what the schema cannot: alternative servers and the
	// security schemes quiver cannot express yet. They are appended to the
	// marshalled file by writeCollection, never marshalled.
	comments []string
}

// security is what one operation needs to know about the document's security:
// which auth profile a scheme name maps to, which headers those profiles will
// supply on their own, and the document-level requirement an operation inherits
// when it declares none.
type security struct {
	profiles map[string]string // scheme name -> auth profile name
	headers  map[string]bool   // lower-cased header names an auth profile owns
	defaults []*base.SecurityRequirement
}

// ownsHeader reports whether an auth profile already supplies this header.
func (s security) ownsHeader(name string) bool { return s.headers[strings.ToLower(name)] }

// mapCollection turns the document's servers and security schemes into the
// collection file, the security index each operation is mapped against, and
// the notes that become the generation report.
func mapCollection(doc *v3high.Document) (Collection, security, []string) {
	c := Collection{Defaults: map[string]string{}}
	sec := security{profiles: map[string]string{}, headers: map[string]bool{}}
	var notes []string

	if doc == nil {
		return c, sec, notes
	}
	sec.defaults = doc.Security

	baseURL, serverNotes := mapServers(doc.Servers, &c)
	c.Defaults["base"] = baseURL
	notes = append(notes, serverNotes...)

	notes = append(notes, mapSecuritySchemes(doc, &c, &sec)...)
	return c, sec, notes
}

// serverVarPattern matches an OpenAPI server-URL variable, `{region}`.
var serverVarPattern = regexp.MustCompile(`\{([^{}]+)\}`)

// mapServers picks the base URL. The first server wins, and every other one is
// written into the file as a commented alternative: a spec with several servers
// is describing environments, and choosing between someone's staging and
// production for them is not the generator's call.
func mapServers(servers []*v3high.Server, c *Collection) (string, []string) {
	if len(servers) == 0 {
		// The specification's own default when `servers` is absent is "/", which
		// is not a URL anything can be sent to. A template is honest about it.
		return "{{base}}", []string{
			"the spec declares no servers; set the base URL with -V base=... or an environment file",
		}
	}

	var notes []string
	urls := make([]string, 0, len(servers))
	for _, s := range servers {
		if s == nil {
			continue
		}
		urls = append(urls, serverURL(s))
	}
	if len(urls) == 0 {
		return "{{base}}", nil
	}

	if len(urls) > 1 {
		c.comments = append(c.comments, "alternative servers declared by the spec:")
		for _, u := range urls[1:] {
			c.comments = append(c.comments, "  base: "+quoteYAML(u))
		}
		notes = append(notes, fmt.Sprintf(
			"the spec declares %d servers; %s was used as defaults.base — "+
				"override the others with -V base=... or an environment file",
			len(urls), urls[0]))
	}
	if strings.Contains(urls[0], "{{") {
		notes = append(notes, fmt.Sprintf(
			"server URL %s has variables; supply them with -V or an environment file", urls[0]))
	} else if !hasScheme(urls[0]) {
		// A relative server URL ("/api/v3") is legal OpenAPI and means "wherever
		// this document is served from" — which the generator does not know. Left
		// unsaid, every generated request would fail with an unhelpful URL error.
		notes = append(notes, fmt.Sprintf(
			"server URL %q is relative to wherever the spec is hosted; "+
				"set the real host with -V base=https://... or an environment file", urls[0]))
	}
	return urls[0], notes
}

// serverURL renders a server entry, turning its server variables into quiver
// templates. A variable with a declared default keeps it — that is the value
// the spec says works.
func serverURL(s *v3high.Server) string {
	return serverVarPattern.ReplaceAllStringFunc(s.URL, func(m string) string {
		name := serverVarPattern.FindStringSubmatch(m)[1]
		if s.Variables != nil {
			if v := s.Variables.GetOrZero(name); v != nil && v.Default != "" {
				return v.Default
			}
		}
		return "{{" + varName(name) + "}}"
	})
}

// mapSecuritySchemes turns securitySchemes into auth profiles.
//
// Every credential is an {{env:...}} reference and never a literal. A generator
// that wrote a placeholder secret into a git-diffable file would be teaching the
// wrong habit at the first moment a user sees its output.
func mapSecuritySchemes(doc *v3high.Document, c *Collection, sec *security) []string {
	if doc.Components == nil || doc.Components.SecuritySchemes == nil {
		return nil
	}
	var notes []string
	for name, scheme := range doc.Components.SecuritySchemes.FromOldest() {
		if scheme == nil {
			continue
		}
		profile, header, note := mapScheme(name, scheme, c)
		if note != "" {
			notes = append(notes, fmt.Sprintf("security scheme %q: %s", name, note))
		}
		if profile == nil {
			continue
		}
		if c.Auth == nil {
			c.Auth = map[string]request.AuthProfile{}
		}
		c.Auth[name] = *profile
		sec.profiles[name] = name
		if header != "" {
			sec.headers[strings.ToLower(header)] = true
		}
	}
	return notes
}

// mapScheme maps one security scheme, returning the profile to write, the
// header that profile will own, and a note for anything not expressible.
func mapScheme(name string, s *v3high.SecurityScheme, c *Collection) (*request.AuthProfile, string, string) {
	env := envPrefix(name)

	switch strings.ToLower(s.Type) {
	case "http":
		switch strings.ToLower(s.Scheme) {
		case "bearer":
			return &request.AuthProfile{
				Type:  "bearer",
				Token: fmt.Sprintf("{{env:%s_TOKEN}}", env),
			}, "Authorization", ""
		case "basic":
			return &request.AuthProfile{
				Type:     "basic",
				Username: fmt.Sprintf("{{env:%s_USERNAME}}", env),
				Password: fmt.Sprintf("{{env:%s_PASSWORD}}", env),
			}, "Authorization", ""
		default:
			return nil, "", fmt.Sprintf(
				"http scheme %q is not supported (quiver has basic and bearer); "+
					"the operations using it will be unauthenticated", s.Scheme)
		}

	case "apikey":
		if strings.EqualFold(s.In, "header") {
			return &request.AuthProfile{
				Type:   "apikey",
				Header: s.Name,
				Key:    fmt.Sprintf("{{env:%s_KEY}}", env),
			}, s.Name, ""
		}
		// A query or cookie API key has no auth-profile shape; saying so beats
		// writing a profile that validates and then sends nothing.
		return nil, "", fmt.Sprintf(
			"apiKey in %q is not supported (auth profiles carry header keys); "+
				"pass %q as a query parameter or cookie yourself", s.In, s.Name)

	case "oauth2", "openidconnect":
		c.comments = append(c.comments,
			fmt.Sprintf("the spec declares %q (%s), which quiver cannot perform yet:",
				name, strings.ToLower(s.Type)),
			fmt.Sprintf("  %s:", name),
			"    type: bearer",
			fmt.Sprintf("    token: \"{{env:%s_TOKEN}}\"   # obtain the token out of band", env))
		return nil, "", fmt.Sprintf(
			"%s is not supported yet; collection.yaml carries a commented bearer "+
				"stub — obtain a token out of band and set %s_TOKEN",
			strings.ToLower(s.Type), env)

	default:
		return nil, "", fmt.Sprintf("type %q is not supported", s.Type)
	}
}

// envPrefixInvalid matches everything an environment-variable name may not
// contain. {{env:NAME}} resolution accepts [A-Za-z_][A-Za-z0-9_]* only, so a
// scheme named "api-key" must not emit {{env:API-KEY_TOKEN}} — that resolves to
// nothing and fails every run with an unresolved-template error.
var envPrefixInvalid = regexp.MustCompile(`[^A-Za-z0-9]+`)

func envPrefix(name string) string {
	p := strings.ToUpper(envPrefixInvalid.ReplaceAllString(name, "_"))
	p = strings.Trim(p, "_")
	if p == "" || (p[0] >= '0' && p[0] <= '9') {
		p = "API_" + p
	}
	return strings.TrimSuffix(p, "_")
}

// quoteYAML renders a string as a double-quoted YAML scalar, for the commented
// alternatives that never pass through the marshaller.
func quoteYAML(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// sortedAuthNames returns the profile names in a stable order, for reports.
func sortedAuthNames(auth map[string]request.AuthProfile) []string {
	names := make([]string, 0, len(auth))
	for n := range auth {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// hasScheme reports whether a URL names a protocol. A server URL without one is
// relative to wherever the document is served from, which a generator run from
// a local file cannot know.
func hasScheme(u string) bool {
	i := strings.Index(u, "://")
	return i > 0 && !strings.ContainsAny(u[:i], "/?#")
}
