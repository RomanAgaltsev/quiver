package request

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseHTTP(t *testing.T) {
	in := []byte(`
name: list users
protocol: http
order: 10
timeout: 5s
http:
  method: GET
  url: "{{base}}/users"
  query: { page: "2" }
captures:
  - var: token
    from: body
    path: data.token
assertions:
  - from: status
    op: eq
    value: "200"
`)
	r, err := Parse(in)
	require.NoError(t, err)
	require.Equal(t, ProtocolHTTP, r.Protocol)
	require.Equal(t, "GET", r.HTTP.Method)
	require.Equal(t, "2", r.HTTP.Query["page"])
	require.Equal(t, 10, *r.Order)
	require.Equal(t, 5*time.Second, r.Timeout.Duration())
	require.Len(t, r.Captures, 1)
	require.Equal(t, "token", r.Captures[0].Var)
	require.NoError(t, r.Validate())
}

// A mistyped key must be an error, not silence. `assertion:` instead of
// `assertions:` previously meant the checks never ran and the command exited 0.
func TestParseRejectsUnknownField(t *testing.T) {
	in := []byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\nassertion:\n  - from: status\n")
	_, err := Parse(in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "assertion")
}

// goccy's []byte unmarshal hook hands back re-serialized YAML *source*, so a
// duration's spelling and its position in the file change what the hook sees
// ("5s\n" mid-document, "\"5s\"" when quoted). Duration uses the
// InterfaceUnmarshaler form to avoid that; these pin every shape.
func TestParseTimeoutForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want time.Duration
	}{
		{"plain, mid-document", "timeout: 5s\nname: x\n", 5 * time.Second},
		{"double-quoted", "timeout: \"1500ms\"\nname: x\n", 1500 * time.Millisecond},
		{"single-quoted", "timeout: '2m'\nname: x\n", 2 * time.Minute},
		{"plain, last line, no trailing newline", "name: x\ntimeout: 90s", 90 * time.Second},
		{"absent", "name: x\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Parse([]byte(tc.in))
			require.NoError(t, err)
			require.Equal(t, tc.want, r.Timeout.Duration())
		})
	}
}

func TestParseRejectsBadTimeout(t *testing.T) {
	_, err := Parse([]byte("name: x\ntimeout: 5 seconds\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout")
}

func TestValidateRejectsMissingBlock(t *testing.T) {
	r := &Request{Name: "x", Protocol: ProtocolGRPC} // no grpc block
	require.Error(t, r.Validate())
}

func TestValidateRejectsUnknownProtocol(t *testing.T) {
	r := &Request{Name: "x", Protocol: Protocol("ftp")}
	require.Error(t, r.Validate())
}

// A typo'd operator must be a config error (exit 2), not a silently failed
// assertion (exit 1) indistinguishable from a real API failure.
func TestValidateRejectsUnknownAssertionOp(t *testing.T) {
	r := &Request{
		Name: "x", Protocol: ProtocolHTTP,
		HTTP:       &HTTPSpec{Method: "GET", URL: "http://x"},
		Assertions: []Assertion{{From: "status", Op: "equals", Value: "200"}},
	}
	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "equals")
}

func TestValidateAssertionsAndCaptures(t *testing.T) {
	base := func() *Request {
		return &Request{Name: "x", Protocol: ProtocolHTTP, HTTP: &HTTPSpec{Method: "GET", URL: "http://x"}}
	}
	t.Run("capture needs a var", func(t *testing.T) {
		r := base()
		r.Captures = []Capture{{From: "status"}}
		require.Error(t, r.Validate())
	})
	t.Run("capture from body needs a path", func(t *testing.T) {
		r := base()
		r.Captures = []Capture{{Var: "v", From: "body"}}
		require.Error(t, r.Validate())
	})
	t.Run("unknown capture source", func(t *testing.T) {
		r := base()
		r.Captures = []Capture{{Var: "v", From: "trailer"}}
		require.Error(t, r.Validate())
	})
	t.Run("eq needs a value", func(t *testing.T) {
		r := base()
		r.Assertions = []Assertion{{From: "status", Op: "eq"}}
		require.Error(t, r.Validate())
	})
	t.Run("matches needs a valid regex", func(t *testing.T) {
		r := base()
		r.Assertions = []Assertion{{From: "body", Path: "n", Op: "matches", Value: "("}}
		require.Error(t, r.Validate())
	})
	t.Run("not_exists is accepted", func(t *testing.T) { // Q11
		r := base()
		r.Assertions = []Assertion{{From: "body", Path: "errors", Op: "not_exists"}}
		require.NoError(t, r.Validate())
	})
}

// A copy-pasted block from another protocol is a mistake, not a no-op.
func TestValidateRejectsForeignBlock(t *testing.T) {
	r := &Request{
		Name: "x", Protocol: ProtocolHTTP,
		HTTP: &HTTPSpec{Method: "GET", URL: "http://x"},
		GRPC: &GRPCSpec{Target: "localhost:1", Method: "a.B/C"},
	}
	require.Error(t, r.Validate())
}

// A body may be inline or read from a file, never both.
func TestValidateRejectsBodyAndBodyFile(t *testing.T) {
	r := &Request{
		Name: "x", Protocol: ProtocolHTTP,
		HTTP: &HTTPSpec{Method: "POST", URL: "http://x", Body: "{}", BodyFile: "b.json"},
	}
	require.Error(t, r.Validate())
}
