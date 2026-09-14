package gen

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"
)

func TestMapCollectionFirstServerBecomesBase(t *testing.T) {
	c, _, _ := mustMapCollection(t, "testdata/servers.yaml")
	require.Equal(t, "https://api.example.com/v1", c.Defaults["base"])
}

func TestMapCollectionMultipleServersAreNoted(t *testing.T) {
	c, _, notes := mustMapCollection(t, "testdata/servers.yaml")
	require.NotEmpty(t, notes, "several servers describe environments; say so")
	require.Contains(t, strings.Join(c.comments, "\n"), "staging.example.com",
		"the alternatives belong in the file, not only in a report nobody keeps")
}

// A server variable with a declared default is a working URL; one without is a
// hole the user has to fill, and the template says so rather than emitting
// something that looks like a URL and is not.
func TestMapCollectionServerVariablesUseTheirDefaults(t *testing.T) {
	c, _, _ := mustMapCollection(t, "testdata/servers.yaml")
	require.Contains(t, strings.Join(c.comments, "\n"), "https://eu.staging.example.com/v1")
}

func TestMapCollectionBearerSchemeUsesAnEnvRef(t *testing.T) {
	c, sec, _ := mustMapCollection(t, "testdata/security-all.yaml")
	p := c.Auth[sec.profiles["bearerAuth"]]
	require.Equal(t, "bearer", p.Type)
	require.Contains(t, p.Token, "{{env:", "a credential must never be a literal")
}

func TestMapCollectionBasicAndAPIKey(t *testing.T) {
	c, sec, _ := mustMapCollection(t, "testdata/security-all.yaml")

	basic := c.Auth[sec.profiles["basicAuth"]]
	require.Equal(t, "basic", basic.Type)
	require.Contains(t, basic.Username, "{{env:")
	require.Contains(t, basic.Password, "{{env:")

	key := c.Auth[sec.profiles["apiKeyAuth"]]
	require.Equal(t, "apikey", key.Type)
	require.Equal(t, "X-API-Key", key.Header)
	require.Contains(t, key.Key, "{{env:")
}

func TestMapCollectionOAuth2IsNotedNotDropped(t *testing.T) {
	c, _, notes := mustMapCollection(t, "testdata/security-all.yaml")
	joined := strings.Join(notes, "\n")
	require.Contains(t, joined, "oauth2",
		"silently dropping it leaves a collection that cannot authenticate")
	require.Contains(t, strings.Join(c.comments, "\n"), "oauth2Auth",
		"the commented stub is where the user finds out what to do about it")
}

// An apiKey in the query string has no auth-profile shape. Emitting a profile
// anyway would validate and then send nothing, and the symptom would be a 401
// that looks like a server problem.
func TestMapCollectionQueryAPIKeyIsNotedNotEmitted(t *testing.T) {
	c, _, notes := mustMapCollection(t, "testdata/security-all.yaml")
	require.NotContains(t, c.Auth, "queryKeyAuth")
	require.Contains(t, strings.Join(notes, "\n"), "api_key")
}

// Every profile written must be one collection.Load will accept; an apikey
// profile with no header name is rejected there, so it must never be generated.
func TestMapCollectionEmitsOnlyValidProfiles(t *testing.T) {
	c, _, _ := mustMapCollection(t, "testdata/security-all.yaml")
	require.NotEmpty(t, c.Auth)
	for _, name := range sortedAuthNames(c.Auth) {
		require.NoError(t, c.Auth[name].Validate(name))
	}
}

// An env reference quiver cannot resolve is worse than none: {{env:API-KEY}}
// matches no secret pattern, so it survives expansion and fails the run.
func TestEnvPrefixIsAlwaysAUsableVariableName(t *testing.T) {
	for in, want := range map[string]string{
		"bearerAuth": "BEARERAUTH",
		"api-key":    "API_KEY",
		"OAuth 2.0":  "OAUTH_2_0",
		"2legged":    "API_2LEGGED",
		"...":        "API",
	} {
		require.Equal(t, want, envPrefix(in), "envPrefix(%q)", in)
	}
}

func TestNoGeneratedByteContainsASecretLiteral(t *testing.T) {
	// The spec fixture carries example credentials. None may reach disk — not in
	// the collection, and not in a request file either.
	doc := mustLoad(t, "testdata/security-all.yaml")
	c, files, _ := Generate(doc)

	out, err := yaml.Marshal(c)
	require.NoError(t, err)
	rendered := []string{string(out), strings.Join(c.comments, "\n")}
	for _, f := range files {
		b, mErr := yaml.Marshal(f.Req)
		require.NoError(t, mErr)
		rendered = append(rendered, string(b))
	}

	all := strings.Join(rendered, "\n")
	for _, secret := range []string{"s3cr3t", "hunter2", "literal-token-value"} {
		require.NotContains(t, all, secret)
	}
}
