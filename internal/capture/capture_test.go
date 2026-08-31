package capture

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

func TestApplyBodyPath(t *testing.T) {
	resp := &core.Response{Status: 200, Body: []byte(`{"data":{"token":"abc"}}`)}
	got, err := Apply([]request.Capture{{Var: "tok", From: "body", Path: "data.token"}}, resp)
	require.NoError(t, err)
	require.Equal(t, "abc", got["tok"])
}

func TestApplyStatusAndHeader(t *testing.T) {
	resp := &core.Response{Status: 201, Headers: map[string][]string{"X-Id": {"42"}}}
	got, err := Apply([]request.Capture{
		{Var: "code", From: "status"},
		{Var: "id", From: "header", Path: "X-Id"},
	}, resp)
	require.NoError(t, err)
	require.Equal(t, "201", got["code"])
	require.Equal(t, "42", got["id"])
}

func TestApplyMissingPath(t *testing.T) {
	resp := &core.Response{Body: []byte(`{}`)}
	_, err := Apply([]request.Capture{{Var: "x", From: "body", Path: "nope"}}, resp)
	require.Error(t, err)
}

// A header explicitly sent as "" is a value, not an absence. Reporting it as
// missing made capture disagree with assert about the same response.
func TestApplyCapturesPresentButEmptyHeader(t *testing.T) {
	resp := &core.Response{Headers: map[string][]string{"X-Empty": {""}}}
	got, err := Apply([]request.Capture{{Var: "v", From: "header", Path: "X-Empty"}}, resp)
	require.NoError(t, err)
	require.Equal(t, "", got["v"])
}

func TestApplyMissingHeaderStillFails(t *testing.T) {
	resp := &core.Response{Headers: map[string][]string{}}
	_, err := Apply([]request.Capture{{Var: "v", From: "header", Path: "X-Missing"}}, resp)
	require.Error(t, err)
}
