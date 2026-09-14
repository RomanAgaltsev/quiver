package gen

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapOperationPathParamsBecomeTemplateVars(t *testing.T) {
	req := mustMap(t, "testdata/params.yaml", "GET", "/pets/{petId}")
	require.Equal(t, "{{base}}/pets/{{petId}}", req.HTTP.URL,
		"a path param must become quiver templating, or the file is not runnable")
}

func TestMapOperationEmitsOnlyRequiredOrExampledQuery(t *testing.T) {
	req := mustMap(t, "testdata/params.yaml", "GET", "/pets")
	require.Equal(t, "10", req.HTTP.Query["limit"])          // has a default
	require.Equal(t, "{{status}}", req.HTTP.Query["status"]) // required, no example
	require.NotContains(t, req.HTTP.Query, "verbose",
		"an optional param with no example is noise, not configuration")
}

func TestMapOperationAssertsTheLowestSuccessStatus(t *testing.T) {
	req := mustMap(t, "testdata/statuses.yaml", "POST", "/pets")
	require.Len(t, req.Assertions, 1)
	require.Equal(t, "status", req.Assertions[0].From)
	require.Equal(t, "eq", req.Assertions[0].Op)
	require.Equal(t, "201", *req.Assertions[0].Value,
		"201 is declared and lower than the 202 also declared")
}

func TestMapOperationNoSuccessStatusAssertsUnder400AndNotes(t *testing.T) {
	req, notes := mapOne(t, "testdata/statuses.yaml", "GET", "/broken")
	require.Equal(t, "lt", req.Assertions[0].Op)
	require.Equal(t, "400", *req.Assertions[0].Value)
	require.NotEmpty(t, notes, "a spec declaring no 2xx is worth reporting")
}

func TestMapOperationBodyPrefersExample(t *testing.T) {
	req := mustMap(t, "testdata/bodies.yaml", "POST", "/pets")
	require.Contains(t, req.HTTP.Body, "Fido", "the declared example must win")
}

func TestMapOperationBodySkeletonIsRequiredFieldsOnly(t *testing.T) {
	req := mustMap(t, "testdata/bodies.yaml", "POST", "/pets-no-example")
	require.Contains(t, req.HTTP.Body, `"name"`)
	require.NotContains(t, req.HTTP.Body, `"nickname"`,
		"an optional property would bury the ones the API insists on")
}

func TestMapOperationSkipsHeadersOwnedByASecurityScheme(t *testing.T) {
	req := mustMap(t, "testdata/security-header.yaml", "GET", "/me")
	require.NotContains(t, req.HTTP.Headers, "Authorization",
		"the auth profile supplies it; emitting it twice is a conflict")
}

func TestMapOperationDoesNotEmitTimeout(t *testing.T) {
	req := mustMap(t, "testdata/params.yaml", "GET", "/pets")
	require.Zero(t, req.Timeout, "a per-operation timeout would be a guess")
}
