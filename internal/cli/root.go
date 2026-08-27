package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is overriden at build time via -ldflags
var Version = "0.0.0-dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "qv",
		Short:         "Quiver - a CLI-first multi-protocol API client (HTTP, gRPC, GraphQL)",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("qv version {{.Version}}\n")
	return root
}

func Execute() int {
	err := newRootCmd().Execute()
	if err != nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "qv:", err)
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.Code()
	}
	return 1
}
