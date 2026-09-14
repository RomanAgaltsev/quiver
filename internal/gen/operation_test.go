package gen

import (
	"regexp"
	"strings"
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

// The plan asked for `op: lt, value: "400"` here. quiver's assertion vocabulary
// has no `lt`, and adding one would be a schema change the plan's own
// constraints forbid — so the generator says the same thing with `matches`,
// which is in the vocabulary and which `qv run` can actually execute. Asserting
// on the emitted operator is the point: an unrunnable file is the failure this
// whole package exists to avoid.
func TestMapOperationNoSuccessStatusAssertsUnder400AndNotes(t *testing.T) {
	req, notes := mapOne(t, "testdata/statuses.yaml", "GET", "/broken")
	require.Equal(t, "matches", req.Assertions[0].Op)
	require.Equal(t, underFourHundred, *req.Assertions[0].Value)
	require.NotEmpty(t, notes, "a spec declaring no 2xx is worth reporting")

	// Whatever it says, it must be a legal assertion.
	req.Path = "broken.yaml"
	require.NoError(t, req.Validate())
}

// The emitted regex has to mean what its name says.
func TestUnderFourHundredMatchesSuccessAndNotFailure(t *testing.T) {
	re := regexp.MustCompile(underFourHundred)
	for _, ok := range []string{"200", "201", "204", "302", "399"} {
		require.True(t, re.MatchString(ok), "%s must count as success", ok)
	}
	for _, bad := range []string{"400", "404", "500", "1000", "20"} {
		require.False(t, re.MatchString(bad), "%s must not count as success", bad)
	}
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

// A self-referential schema is ordinary, not exotic: without a depth cap the
// skeleton walk would not terminate.
func TestMapOperationRecursiveSchemaTerminatesAndIsNoted(t *testing.T) {
	req, notes := mapOne(t, "testdata/bodies.yaml", "POST", "/pets-recursive")
	require.Contains(t, req.HTTP.Body, `"name"`)
	require.Contains(t, strings.Join(notes, "\n"), "recursive")
}

// `security: []` is the one place an empty array means something: the operation
// is explicitly public and must not inherit the document's security.
func TestMapOperationExplicitlyPublicGetsNoAuth(t *testing.T) {
	req := mustMap(t, "testdata/security-header.yaml", "GET", "/public")
	require.Empty(t, req.Auth)
}

func TestMapOperationWithASecuritySchemeReferencesItsProfile(t *testing.T) {
	req := mustMap(t, "testdata/security-header.yaml", "GET", "/me")
	require.Equal(t, "bearerAuth", req.Auth)
	require.Equal(t, "{{X-Trace}}", req.HTTP.Headers["X-Trace"],
		"a required header with no example still has to be suppliable")
}

// Every generated request must be a legal one. This is the cheap half of the
// end-to-end promise; the expensive half is running it.
func TestMappedOperationsAreValidRequests(t *testing.T) {
	for _, fixture := range []string{
		"testdata/minimal.yaml", "testdata/multi-method.yaml", "testdata/params.yaml",
		"testdata/statuses.yaml", "testdata/bodies.yaml", "testdata/security-header.yaml",
		"testdata/servers.yaml", "testdata/security-all.yaml",
	} {
		_, files, _ := Generate(mustLoad(t, fixture))
		require.NotEmpty(t, files, fixture)
		for _, f := range files {
			require.NoError(t, f.Req.Validate(), "%s: %s", fixture, f.Path)
		}
	}
}
