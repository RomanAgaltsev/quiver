package env

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

func TestExpand(t *testing.T) {
	got, err := (&Resolved{Vars: map[string]string{"base": "http://x", "id": "7"}}).Expand("{{base}}/users/{{id}}")
	require.NoError(t, err)
	require.Equal(t, "http://x/users/7", got)
}

func TestExpandMissingVar(t *testing.T) {
	_, err := (&Resolved{Vars: map[string]string{}}).Expand("{{nope}}")
	require.Error(t, err)
	require.Contains(t, err.Error(), "nope")
}

// A captured value containing a quote or a backslash must not be able to
// break the JSON body it is interpolated into.
func TestExpandJSONFilter(t *testing.T) {
	vars := map[string]string{"name": `he said "hi"\n`}
	got, err := (&Resolved{Vars: vars}).Expand(`{"name":{{name | json}}}`)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"he said \"hi\"\\n"}`, got)
}

func TestExpandUnknownFilter(t *testing.T) {
	_, err := (&Resolved{Vars: map[string]string{"v": "x"}}).Expand("{{v | base64}}")
	require.Error(t, err)
}

// {{env:NAME}} reads the process environment and marks the value secret.
func TestMergeVarsResolvesSecretRefs(t *testing.T) {
	t.Setenv("QV_TEST_TOKEN", "s3cret")

	res, err := MergeVars(
		map[string]string{"base": "http://default"},
		map[string]string{"base": "http://env", "token": "{{env:QV_TEST_TOKEN}}"},
		map[string]string{"base": "http://override"},
	)
	require.NoError(t, err)
	require.Equal(t, "http://override", res.Vars["base"]) // defaults < env < --var
	require.Equal(t, "s3cret", res.Vars["token"])
	require.Equal(t, []string{"s3cret"}, res.Secrets)
}

func TestMergeVarsMissingSecretIsConfigError(t *testing.T) {
	_, err := MergeVars(nil, map[string]string{"token": "{{env:QV_DEFINITELY_UNSET}}"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "QV_DEFINITELY_UNSET")
}

func TestResolveHTTP(t *testing.T) {
	r := &request.Request{
		Name:     "u",
		Protocol: request.ProtocolHTTP,
		HTTP:     &request.HTTPSpec{Method: "GET", URL: "{{base}}/users", Headers: map[string]string{"X-Env": "{{envname}}"}},
	}
	res := &Resolved{Vars: map[string]string{"base": "http://x", "envname": "dev"}}
	rr, err := Resolve(r, res, nil)
	require.NoError(t, err)
	require.Equal(t, "http://x/users", rr.HTTP.URL)
	require.Equal(t, "dev", rr.HTTP.Headers["X-Env"])
}

// A per-request timeout reaches the executor via ResolvedRequest.
func TestResolveCarriesTimeout(t *testing.T) {
	r := &request.Request{Name: "u", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "http://x"}}
	require.NoError(t, yamlInto(r, "timeout: 250ms"))
	rr, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
	require.NoError(t, err)
	require.Equal(t, 250*time.Millisecond, rr.Timeout)
}

func yamlInto(r *request.Request, s string) error {
	return yaml.Unmarshal([]byte(s), r)
}

// body_file is read relative to the request file and expanded like an inline body.
func TestResolveBodyFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "body.json"), []byte(`{"n":"{{who}}"}`), 0o644))

	r := &request.Request{Name: "c", Protocol: request.ProtocolHTTP,
		Path: filepath.Join(dir, "create.yaml"),
		HTTP: &request.HTTPSpec{Method: "POST", URL: "http://x", BodyFile: "body.json"}}
	rr, err := Resolve(r, &Resolved{Vars: map[string]string{"who": "ada"}}, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"n":"ada"}`, rr.HTTP.Body)
}

func TestResolveAuthProfile(t *testing.T) {
	r := &request.Request{Name: "u", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "http://x"}, Auth: "main"}
	auth := map[string]request.AuthProfile{"main": {Type: "bearer", Token: "{{tok}}"}}
	rr, err := Resolve(r, &Resolved{Vars: map[string]string{"tok": "abc"}}, auth)
	require.NoError(t, err)
	require.Equal(t, "abc", rr.Auth.Token)
}

func TestResolveUnknownAuthProfile(t *testing.T) {
	r := &request.Request{Name: "u", Protocol: request.ProtocolHTTP,
		HTTP: &request.HTTPSpec{Method: "GET", URL: "http://x"}, Auth: "nope"}
	_, err := Resolve(r, &Resolved{Vars: map[string]string{}}, nil)
	require.Error(t, err)
}
