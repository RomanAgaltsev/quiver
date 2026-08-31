package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Registry.Close deduplicated with a map[Executor]bool, and ExecutorFunc is a
// func type — unhashable, so using one as a map key panics at runtime. The type
// this package documents as "the one test seam the design actually needs" was
// the one that crashed Close.
func TestRegistryCloseSurvivesFuncExecutors(t *testing.T) {
	reg := core.Registry{
		request.ProtocolHTTP: core.ExecutorFunc(
			func(context.Context, core.ResolvedRequest) (*core.Response, error) { return nil, nil }),
	}
	require.NotPanics(t, func() { require.NoError(t, reg.Close()) })
}

// The same executor may serve several protocols (httpx backs both http and
// graphql) and must be closed exactly once.
func TestRegistryCloseDeduplicatesSharedExecutor(t *testing.T) {
	var calls int
	ex := &closerExecutor{onClose: func() error { calls++; return nil }}
	reg := core.Registry{
		request.ProtocolHTTP:    ex,
		request.ProtocolGraphQL: ex,
	}
	require.NoError(t, reg.Close())
	require.Equal(t, 1, calls, "a shared executor must not be closed twice")
}

type closerExecutor struct{ onClose func() error }

func (e *closerExecutor) Execute(context.Context, core.ResolvedRequest) (*core.Response, error) {
	return nil, nil
}
func (e *closerExecutor) Close() error { return e.onClose() }

func TestRegistryCloseJoinsErrors(t *testing.T) {
	boom := errors.New("boom")
	reg := core.Registry{
		request.ProtocolGRPC: &closerExecutor{onClose: func() error { return boom }},
	}
	require.ErrorIs(t, reg.Close(), boom)
}

// Presence and emptiness are different questions; conflating them made capture
// and assert disagree about the same response.
func TestHeaderPresentDistinguishesEmptyFromAbsent(t *testing.T) {
	resp := &core.Response{Headers: map[string][]string{"X-Empty": {""}}}

	v, ok := resp.HeaderPresent("X-Empty")
	require.True(t, ok, "a header sent as \"\" is present")
	require.Equal(t, "", v)

	_, ok = resp.HeaderPresent("X-Missing")
	require.False(t, ok)
}

func TestConfigErrorUnwraps(t *testing.T) {
	inner := errors.New("bad variable")
	err := core.NewConfigError(inner)
	require.True(t, core.IsConfigError(err))
	require.ErrorIs(t, err, inner)
	require.Nil(t, core.NewConfigError(nil))
	require.False(t, core.IsConfigError(inner))
}
