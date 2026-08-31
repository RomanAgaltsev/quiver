package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/secret"
)

var update = flag.Bool("update", false, "update golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run `go test ./internal/render/... -update` to create it")
	require.Equal(t, string(want), string(got))
}

func sample() *core.Response {
	return &core.Response{
		Protocol: request.ProtocolHTTP, Status: 200, StatusText: "200 OK", OK: true,
		Headers: map[string][]string{"Content-Type": {"application/json"}},
		Body:    []byte(`{"ok":true,"n":0}`),
	}
}

func opts(format string) Options {
	return Options{Format: format, Redactor: secret.NewRedactor(nil)}
}

func TestRenderRaw(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, sample(), opts("raw")))
	require.Equal(t, `{"ok":true,"n":0}`, buf.String())
}

func TestRenderJSONGolden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, sample(), opts("json")))
	golden(t, "json", buf.Bytes())
}

func TestRenderPrettyGolden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, sample(), opts("pretty")))
	golden(t, "pretty", buf.Bytes())
}

// Q27: headers were invisible in pretty output, and --verbose was never implemented.
func TestRenderPrettyVerboseShowsHeaders(t *testing.T) {
	o := opts("pretty")
	o.Verbose = true
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, sample(), o))
	require.Contains(t, buf.String(), "Content-Type: application/json")
}

// Q5: no rendering path may leak a secret.
func TestRenderRedactsSecrets(t *testing.T) {
	resp := sample()
	resp.Body = []byte(`{"token":"s3cret"}`)
	resp.Headers = map[string][]string{"Authorization": {"Bearer s3cret"}}

	for _, format := range []string{"raw", "json", "pretty"} {
		o := Options{Format: format, Verbose: true, Redactor: secret.NewRedactor([]string{"s3cret"})}
		var buf bytes.Buffer
		require.NoError(t, Render(&buf, resp, o))
		require.NotContains(t, buf.String(), "s3cret", "format %q leaked a secret", format)
		require.Contains(t, buf.String(), "***")
	}
}

// --dry-run prints the resolved request and sends nothing — and it is
// redacted too, since that is exactly where a resolved token would show up.
func TestDryRunPrintsResolvedRequestRedacted(t *testing.T) {
	rr := &core.ResolvedRequest{
		Name: "login", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "POST", URL: "https://api/login",
			Headers: map[string]string{"Authorization": "Bearer s3cret"}, Body: `{"u":"ada"}`},
	}
	var buf bytes.Buffer
	require.NoError(t, DryRun(&buf, rr, Options{Format: "pretty", Redactor: secret.NewRedactor([]string{"s3cret"})}))
	out := buf.String()
	require.Contains(t, out, "POST https://api/login")
	require.NotContains(t, out, "s3cret")
	require.Contains(t, out, "***")
}

func TestRenderUnknownFormat(t *testing.T) {
	require.Error(t, Render(&bytes.Buffer{}, &core.Response{}, opts("xml")))
}

// --dry-run must show the URL that would actually be requested, query merge
// included, and must not render query params in header syntax — the previous
// revision reused the header writer, so `page: 2` looked like a header.
func TestDryRunShowsEffectiveURLAndDoesNotFakeHeaders(t *testing.T) {
	rr := &core.ResolvedRequest{
		Name: "list", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "https://api/users",
			Headers: map[string]string{"Accept": "application/json"},
			Query:   map[string]string{"page": "2"}},
	}
	var buf bytes.Buffer
	require.NoError(t, DryRun(&buf, rr, opts("pretty")))
	out := buf.String()
	require.Contains(t, out, "GET https://api/users?page=2")
	require.Contains(t, out, "Accept: application/json")
	require.NotContains(t, out, "page: 2", "a query param must not be printed as a header")
}

// gRPC metadata is prefixed so it cannot be mistaken for HTTP headers.
func TestDryRunLabelsGRPCMetadata(t *testing.T) {
	rr := &core.ResolvedRequest{
		Name: "echo", Protocol: request.ProtocolGRPC,
		GRPC: &request.GRPCSpec{Target: "h:1", Method: "s.S/M",
			Metadata: map[string]string{"x-trace": "abc"}, Message: `{"a":1}`},
	}
	var buf bytes.Buffer
	require.NoError(t, DryRun(&buf, rr, opts("pretty")))
	require.Contains(t, buf.String(), "metadata x-trace: abc")
}

// Spec §3 promises syntax-highlighted pretty output. Only the status line was
// coloured; the body went out plain.
func TestPrettyHighlightsJSONBodyWhenColoured(t *testing.T) {
	o := opts("pretty")
	o.Color = true
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, sample(), o))
	out := buf.String()
	require.Contains(t, out, "\x1b[", "a coloured render must emit ANSI escapes")
	// The document itself must survive highlighting untouched once stripped.
	require.Contains(t, stripANSI(out), "\"ok\": true")
}

// Highlighting must never let a secret through: redaction happens first, so an
// escape sequence cannot split a secret out of the redactor's reach.
func TestHighlightedOutputStillRedacts(t *testing.T) {
	resp := sample()
	resp.Body = []byte(`{"token":"s3cret"}`)
	o := Options{Format: "pretty", Color: true, Redactor: secret.NewRedactor([]string{"s3cret"})}
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, resp, o))
	require.NotContains(t, buf.String(), "s3cret")
}

// A body that is not JSON is passed through rather than mangled by the scanner.
func TestPrettyLeavesNonJSONBodyAlone(t *testing.T) {
	resp := sample()
	resp.Body = []byte("plain text {not json")
	o := opts("pretty")
	o.Color = true
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, resp, o))
	require.Contains(t, buf.String(), "plain text {not json")
}

func stripANSI(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			i++ // the 'm'
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// A non-JSON body is embedded as a JSON string rather than producing an
// invalid document.
func TestRenderJSONQuotesNonJSONBody(t *testing.T) {
	resp := sample()
	resp.Body = []byte("not json at all")
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, resp, opts("json")))
	require.Contains(t, buf.String(), `"body": "not json at all"`)
}

func TestDryRunGraphQLAndAuth(t *testing.T) {
	rr := &core.ResolvedRequest{
		Name: "search", Protocol: request.ProtocolGraphQL,
		GraphQL: &request.GraphQLSpec{URL: "https://api/graphql",
			Headers:   map[string]string{"Accept": "application/json"},
			Query:     "query Hero { hero { name } }",
			Variables: `{"ep":"JEDI"}`},
		Auth: &request.AuthProfile{Type: "bearer", Token: "t0k"},
	}
	var buf bytes.Buffer
	require.NoError(t, DryRun(&buf, rr, opts("pretty")))
	out := buf.String()
	require.Contains(t, out, "POST https://api/graphql")
	require.Contains(t, out, "Accept: application/json")
	require.Contains(t, out, "query Hero")
	require.Contains(t, out, `variables: {"ep":"JEDI"}`)
	require.Contains(t, out, "(auth: bearer)")
}

func TestDryRunRejectsAnUnparseableURL(t *testing.T) {
	rr := &core.ResolvedRequest{Name: "x", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "http://[::1]:namedport/"}}
	require.Error(t, DryRun(&bytes.Buffer{}, rr, opts("pretty")))
}

// NO_COLOR is honoured regardless of the writer (https://no-color.org).
func TestShouldColorHonoursNoColorAndNonFiles(t *testing.T) {
	require.False(t, ShouldColor(&bytes.Buffer{}), "a non-file writer is never a TTY")

	t.Setenv("NO_COLOR", "1")
	require.False(t, ShouldColor(os.Stdout))
}

// A string containing an escaped quote must not end the literal early, or the
// highlighter would mis-classify everything after it.
func TestHighlightHandlesEscapedQuotes(t *testing.T) {
	resp := sample()
	resp.Body = []byte(`{"msg":"he said \"hi\"","n":1}`)
	o := opts("pretty")
	o.Color = true
	var buf bytes.Buffer
	require.NoError(t, Render(&buf, resp, o))
	require.Contains(t, stripANSI(buf.String()), `"he said \"hi\""`)
}
