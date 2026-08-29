package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
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
