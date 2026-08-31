package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/secret"
)

// varPattern's name class excludes the colon, so a {{env:NAME}} written inside a
// request file used to match nothing at all: it was neither expanded nor
// reported, and the literal text went out as an Authorization header.
func TestExpandResolvesInlineSecretRef(t *testing.T) {
	t.Setenv("QV_TEST_TOKEN", "s3cret")

	res := &Resolved{Vars: map[string]string{}}
	got, err := res.Expand("Bearer {{env:QV_TEST_TOKEN}}")
	require.NoError(t, err)
	require.Equal(t, "Bearer s3cret", got)
	require.Contains(t, res.Secrets, "s3cret", "an inline secret must be redactable")
}

// A secret discovered during resolution must reach the redactor that render and
// history were already handed, or it is resolved-but-printed — strictly worse
// than left unresolved.
func TestInlineSecretRefReachesTheAttachedRedactor(t *testing.T) {
	t.Setenv("QV_TEST_TOKEN", "s3cret")

	red := secret.NewRedactor(nil)
	res := &Resolved{Vars: map[string]string{}, Redactor: red}
	_, err := res.Expand("{{env:QV_TEST_TOKEN}}")
	require.NoError(t, err)
	require.Equal(t, "Bearer ***", red.String("Bearer s3cret"))
}

func TestExpandRejectsUnsetInlineSecretRef(t *testing.T) {
	res := &Resolved{Vars: map[string]string{}}
	_, err := res.Expand("Bearer {{env:QV_DEFINITELY_UNSET}}")
	require.Error(t, err)
	require.Contains(t, err.Error(), "QV_DEFINITELY_UNSET")
	require.True(t, core.IsConfigError(err))
}

// Expand substitutes once and does not recurse, so a variable whose value is
// itself a template used to pass through silently and go on the wire.
func TestExpandRejectsNestedTemplate(t *testing.T) {
	res := &Resolved{Vars: map[string]string{"base": "https://{{host}}", "host": "example.com"}}
	_, err := res.Expand("{{base}}/x")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpanded template")
	require.True(t, core.IsConfigError(err))
}

// Brace-heavy content that is not a template must not trip the leftover check:
// a compact GraphQL selection set and a nested JSON body are both legitimate.
func TestExpandLeavesBraceHeavyContentAlone(t *testing.T) {
	res := &Resolved{Vars: map[string]string{"n": "1"}}
	for _, in := range []string{
		`{ hero { name friends { id } } }`,
		`{"a":{"b":[1,2]},"c":{{n}}}`,
	} {
		_, err := res.Expand(in)
		require.NoError(t, err, "input %q", in)
	}
}

// Every resolve failure is the definition's fault, so all of them must be
// config errors — spec §8 maps those to exit 2, not 1.
func TestResolveFailuresAreConfigErrors(t *testing.T) {
	base := func() *request.Request {
		return &request.Request{Name: "u", Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "GET", URL: "http://x"}}
	}

	t.Run("unresolved variable", func(t *testing.T) {
		r := base()
		r.HTTP.URL = "{{nope}}/x"
		_, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
		require.True(t, core.IsConfigError(err))
	})

	t.Run("unknown auth profile", func(t *testing.T) {
		r := base()
		r.Auth = "nope"
		_, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
		require.True(t, core.IsConfigError(err))
	})

	t.Run("missing body_file", func(t *testing.T) {
		r := base()
		r.HTTP.BodyFile = "does-not-exist.json"
		_, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
		require.True(t, core.IsConfigError(err))
	})
}

// A shallow struct copy shares the slice's backing array, so rewriting
// proto_files in place mutated the caller's loaded Request.
func TestResolveDoesNotMutateProtoFiles(t *testing.T) {
	r := &request.Request{Name: "g", Protocol: request.ProtocolGRPC,
		Path: "col/requests/echo.yaml",
		GRPC: &request.GRPCSpec{Target: "x:1", Method: "s.S/M",
			ProtoFiles: []string{"protos/echo.proto"}}}

	rr, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"protos/echo.proto"}, r.GRPC.ProtoFiles, "the loaded request must be untouched")
	require.NotEqual(t, r.GRPC.ProtoFiles, rr.GRPC.ProtoFiles)
}

func TestExpandMapAndExpandAll(t *testing.T) {
	res := &Resolved{Vars: map[string]string{"a": "1", "b": "2"}}

	got, err := res.ExpandMap(map[string]string{"x": "{{a}}", "y": "{{b}}"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"x": "1", "y": "2"}, got)

	nilMap, err := res.ExpandMap(nil)
	require.NoError(t, err)
	require.Nil(t, nilMap)

	_, err = res.ExpandMap(map[string]string{"x": "{{nope}}"})
	require.Error(t, err)

	s1, s2, empty := "{{a}}", "{{b}}", ""
	require.NoError(t, res.ExpandAll(&s1, &s2, &empty, nil))
	require.Equal(t, "1", s1)
	require.Equal(t, "2", s2)

	bad := "{{nope}}"
	require.Error(t, res.ExpandAll(&bad))
}

func TestAddSecretDeduplicatesAndOrders(t *testing.T) {
	res := &Resolved{}
	res.AddSecret("short")
	res.AddSecret("a-much-longer-secret")
	res.AddSecret("short") // duplicate
	res.AddSecret("")      // ignored: replacing "" would corrupt everything
	require.Equal(t, []string{"a-much-longer-secret", "short"}, res.Secrets)
}

func TestLoadEnvironmentErrors(t *testing.T) {
	_, err := LoadEnvironment(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	require.True(t, core.IsConfigError(err))

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("::: not yaml :::\n"), 0o644))
	_, err = LoadEnvironment(bad)
	require.Error(t, err)
	require.True(t, core.IsConfigError(err))
}

// Numbers and booleans in an environment file are coerced to strings, so a port
// or a flag does not have to be quoted.
func TestLoadEnvironmentCoercesScalars(t *testing.T) {
	p := filepath.Join(t.TempDir(), "dev.yaml")
	require.NoError(t, os.WriteFile(p, []byte("port: 8080\ndebug: true\nbase: http://x\n"), 0o644))
	m, err := LoadEnvironment(p)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"port": "8080", "debug": "true", "base": "http://x"}, m)
}

func TestResolveGRPCAndGraphQL(t *testing.T) {
	res := &Resolved{Vars: map[string]string{"host": "h:1", "id": "7", "base": "http://x"}}

	g := &request.Request{Name: "g", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "{{host}}", Method: "s.S/M",
			Message:  `{"id":"{{id}}"}`,
			Metadata: map[string]string{"x-id": "{{id}}"}}}
	rr, err := Resolve(g, res, nil)
	require.NoError(t, err)
	require.Equal(t, "h:1", rr.GRPC.Target)
	require.JSONEq(t, `{"id":"7"}`, rr.GRPC.Message)
	require.Equal(t, "7", rr.GRPC.Metadata["x-id"])

	q := &request.Request{Name: "q", Protocol: request.ProtocolGraphQL,
		GraphQL: &request.GraphQLSpec{URL: "{{base}}/graphql", Query: "query { a }",
			Variables: `{"id":"{{id}}"}`,
			Headers:   map[string]string{"X-Id": "{{id}}"}}}
	rr, err = Resolve(q, res, nil)
	require.NoError(t, err)
	require.Equal(t, "http://x/graphql", rr.GraphQL.URL)
	require.JSONEq(t, `{"id":"7"}`, rr.GraphQL.Variables)
	require.Equal(t, "7", rr.GraphQL.Headers["X-Id"])
}

func TestResolveUnknownProtocolIsAConfigError(t *testing.T) {
	r := &request.Request{Name: "x", Protocol: request.Protocol("smtp")}
	_, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
	require.True(t, core.IsConfigError(err))
}
