package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootVersion(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--version"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "qv version")
}

// A config error must keep its cause so the user sees a real diagnostic,
// not the string "exit code 2".
func TestConfigErrPreservesCause(t *testing.T) {
	err := configErr(errors.New("unknown environment \"stage\""))
	require.EqualError(t, err, `unknown environment "stage"`)

	var ee *exitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 2, ee.Code())
}

// Usage text belongs to usage errors only, never to runtime failures.
func TestRootSilencesUsageAndErrors(t *testing.T) {
	root := newRootCmd()
	require.True(t, root.SilenceUsage)
	require.True(t, root.SilenceErrors)
}
