package httpx

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// capture runs one request against a recording server and hands the test the
// request the server actually saw.
func capture(t *testing.T, build func(url string) core.ResolvedRequest) *http.Request {
	t.Helper()
	var seen *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		clone := r.Clone(context.Background())
		seen = clone
	}))
	t.Cleanup(srv.Close)

	_, err := New().Execute(context.Background(), build(srv.URL))
	require.NoError(t, err)
	require.NotNil(t, seen)
	return seen
}

// Go's http.Client infers no content type, so a body used to go out with none at
// all — and most frameworks answer that with a 415. curl defaults to form
// encoding and HTTPie to JSON; sending nothing is the one option that fails.
func TestHTTPSetsDefaultContentTypeForJSONBody(t *testing.T) {
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "POST", URL: url, Body: `{"a":1}`}}
	})
	require.Equal(t, "application/json", got.Header.Get("Content-Type"))
}

func TestHTTPSetsTextContentTypeForNonJSONBody(t *testing.T) {
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "POST", URL: url, Body: "hello"}}
	})
	require.Equal(t, "text/plain; charset=utf-8", got.Header.Get("Content-Type"))
}

func TestHTTPExplicitContentTypeWins(t *testing.T) {
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "POST", URL: url, Body: `{"a":1}`,
				Headers: map[string]string{"Content-Type": "text/csv"}}}
	})
	require.Equal(t, "text/csv", got.Header.Get("Content-Type"))
}

func TestHTTPNoBodyNoContentType(t *testing.T) {
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "GET", URL: url}}
	})
	require.Empty(t, got.Header.Get("Content-Type"))
}

// Nothing in the original suite asserted that an auth profile reached the wire,
// for any protocol — which is how gRPC came to ignore rr.Auth entirely.
func TestHTTPAuthProfileReachesTheWire(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile request.AuthProfile
		header  string
		want    string
	}{
		{"bearer", request.AuthProfile{Type: "bearer", Token: "t0k"}, "Authorization", "Bearer t0k"},
		{"basic", request.AuthProfile{Type: "basic", Username: "ada", Password: "pw"}, "Authorization",
			"Basic " + base64.StdEncoding.EncodeToString([]byte("ada:pw"))},
		{"apikey", request.AuthProfile{Type: "apikey", Header: "X-API-Key", Key: "k3y"}, "X-Api-Key", "k3y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capture(t, func(url string) core.ResolvedRequest {
				return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
					HTTP: &request.HTTPSpec{Method: "GET", URL: url},
					Auth: &tc.profile}
			})
			require.Equal(t, tc.want, got.Header.Get(tc.header))
		})
	}
}

// An explicit Authorization header is more specific than the profile the request
// merely references, so the profile must not silently overwrite it.
func TestHTTPExplicitAuthorizationHeaderWins(t *testing.T) {
	profile := request.AuthProfile{Type: "bearer", Token: "from-profile"}
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "GET", URL: url,
				Headers: map[string]string{"Authorization": "Bearer explicit"}},
			Auth: &profile}
	})
	require.Equal(t, "Bearer explicit", got.Header.Get("Authorization"))
}

// url.Values.Encode sorts keys and re-percent-encodes, which silently breaks
// signed URLs and order-sensitive filter DSLs. With no `query:` block there is
// nothing to merge, so the string must survive byte for byte.
func TestHTTPLeavesHandWrittenQueryAlone(t *testing.T) {
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "GET", URL: url + "/x?z=1&a=2&z=3"}}
	})
	require.Equal(t, "z=1&a=2&z=3", got.URL.RawQuery)
}

// Merging adds to the existing query rather than replacing it: `?tag=x` plus
// query:{tag: y} means both, the way a repeated flag does everywhere else.
func TestHTTPQueryMergeAddsRatherThanReplaces(t *testing.T) {
	got := capture(t, func(url string) core.ResolvedRequest {
		return core.ResolvedRequest{Protocol: request.ProtocolHTTP,
			HTTP: &request.HTTPSpec{Method: "GET", URL: url + "/x?tag=a",
				Query: map[string]string{"tag": "b"}}}
	})
	require.Equal(t, []string{"a", "b"}, got.URL.Query()["tag"])
}

// EffectiveURL is what both the executor and --dry-run print, so a preview can
// never disagree with the wire.
func TestEffectiveURL(t *testing.T) {
	for _, tc := range []struct{ name, url, want string }{
		{"untouched without query", "https://x/y?b=2&a=1", "https://x/y?b=2&a=1"},
		{"merged when query set", "https://x/y", "https://x/y?p=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := &request.HTTPSpec{URL: tc.url}
			if tc.name == "merged when query set" {
				spec.Query = map[string]string{"p": "1"}
			}
			got, err := spec.EffectiveURL()
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
