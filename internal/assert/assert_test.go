package assert

import (
	"testing"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/stretchr/testify/require"
)

func TestRunStatusEq(t *testing.T) {
	resp := &core.Response{Status: 200, Body: []byte(`{"ok":true}`)}
	rs, err := Run([]request.Assertion{{From: "status", Op: "eq", Value: "200"}}, resp)
	require.NoError(t, err)
	require.True(t, AllPassed(rs))
}

func TestRunBodyExistsAndContains(t *testing.T) {
	resp := &core.Response{Status: 200, Body: []byte(`{"name":"quiver"}`)}
	rs, err := Run([]request.Assertion{
		{From: "body", Path: "name", Op: "exists"},
		{From: "body", Path: "name", Op: "contains", Value: "quiv"},
	}, resp)
	require.NoError(t, err)
	require.True(t, AllPassed(rs))
}

func TestRunFails(t *testing.T) {
	resp := &core.Response{Status: 500}
	rs, err := Run([]request.Assertion{{From: "status", Op: "eq", Value: "200"}}, resp)
	require.NoError(t, err)
	require.False(t, AllPassed(rs))
}

// `exists` must mean "the field is present", not "the field is non-empty".
// The previous revision implemented it as actual != "", so a present-but-empty
// field was reported absent — and `capture` (which uses gjson's Exists) disagreed
// with `assert` about the same response.
func TestExistsIsAboutPresenceNotEmptiness(t *testing.T) {
	resp := &core.Response{Status: 200, Body: []byte(`{"name":"","count":0}`)}

	rs, err := Run([]request.Assertion{
		{Name: "empty string exists", From: "body", Path: "name", Op: "exists"},
		{Name: "zero exists", From: "body", Path: "count", Op: "exists"},
		{Name: "missing does not exist", From: "body", Path: "nope", Op: "not_exists"},
	}, resp)
	require.NoError(t, err)
	require.True(t, AllPassed(rs), "%+v", rs)
}

// Asserting absence is what makes a GraphQL `errors` check expressible.
func TestNotExists(t *testing.T) {
	clean := &core.Response{Status: 200, Body: []byte(`{"data":{"x":1}}`)}
	rs, err := Run([]request.Assertion{{From: "body", Path: "errors", Op: "not_exists"}}, clean)
	require.NoError(t, err)
	require.True(t, AllPassed(rs))

	failed := &core.Response{Status: 200, Body: []byte(`{"errors":[{"message":"boom"}]}`)}
	rs, err = Run([]request.Assertion{{From: "body", Path: "errors", Op: "not_exists"}}, failed)
	require.NoError(t, err)
	require.False(t, AllPassed(rs))
}

func TestMatches(t *testing.T) {
	resp := &core.Response{Status: 200, Body: []byte(`{"id":"user_a1b2"}`)}
	rs, err := Run([]request.Assertion{{From: "body", Path: "id", Op: "matches", Value: `^user_[a-z0-9]+$`}}, resp)
	require.NoError(t, err)
	require.True(t, AllPassed(rs))
}

// gRPC status may be asserted by code name as well as by number, because
// value: "0" is unreadable next to the HTTP habit of value: "200".
func TestStatusAcceptsGRPCCodeName(t *testing.T) {
	resp := &core.Response{Protocol: request.ProtocolGRPC, Status: 0, StatusText: "OK", OK: true}
	rs, err := Run([]request.Assertion{
		{From: "status", Op: "eq", Value: "OK"},
		{From: "status", Op: "eq", Value: "0"},
	}, resp)
	require.NoError(t, err)
	require.True(t, AllPassed(rs), "%+v", rs)
}

// A header that is absent must fail `exists` rather than quietly comparing "".
func TestHeaderExistence(t *testing.T) {
	resp := &core.Response{Headers: map[string][]string{"X-Id": {"42"}}}
	rs, err := Run([]request.Assertion{
		{From: "header", Path: "X-Id", Op: "exists"},
		{From: "header", Path: "X-Missing", Op: "not_exists"},
	}, resp)
	require.NoError(t, err)
	require.True(t, AllPassed(rs))
}
