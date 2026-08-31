package env

import (
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
