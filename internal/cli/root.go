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
		Use:     "qv",
		Short:   "Quiver — a CLI-first multi-protocol API client (HTTP, gRPC, GraphQL)",
		Version: Version,
		// Q4: usage is for usage errors. A failed request is not a usage error,
		// and Execute prints the cause itself, exactly once.
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("qv version {{.Version}}\n")

	pf := root.PersistentFlags()
	pf.StringP("output", "o", "pretty", "output format: pretty|raw|json")
	pf.String("env", "", "environment name or file")
	pf.String("collection", "", "collection root (default: search upward for collection.yaml)")
	pf.StringArrayP("var", "V", nil, "override variable, key=value (repeatable)")
	pf.Duration("timeout", 0, "per-request timeout override (e.g. 5s)")
	pf.BoolP("verbose", "v", false, "show response headers and timings")
	pf.Bool("quiet", false, "suppress response output; exit code only")
	pf.Bool("insecure", false, "skip TLS certificate verification")
	pf.Bool("show-secrets", false, "do not redact secret values (debugging only)")
	pf.Bool("check-status", false, "fail on any non-2xx / non-OK / GraphQL-error response")
	pf.Bool("dry-run", false, "print the resolved request without sending it")

	root.AddCommand(
		newRunCmd(),
		newHTTPCmd(),
		newGRPCCmd(),
		newGraphQLCmd(),
		newInitCmd(),
		newNewCmd(),
		newEnvCmd(),
		newHistoryCmd(),
	)
	return root
}

func Execute() int {
	err := newRootCmd().Execute()
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, "qv:", err)
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.Code()
	}
	return 1
}
