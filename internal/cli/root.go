// Package cli wires the cobra commands over the library packages: flag
// parsing, the shared run context, output, and exit codes.
package cli

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// devVersion is the placeholder an unstamped build reports.
const devVersion = "0.0.0-dev"

// Version is overridden at build time via -ldflags. Prefer version().
var Version = devVersion

// version reports the build's version. `go install …@latest` — the README's
// primary install path — links no ldflags, so fall back to the module version
// the Go toolchain stamps into the binary rather than always claiming 0.0.0-dev
// and leaving bug reports unable to name a release.
func version() string {
	if Version != devVersion {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := bi.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return Version
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "qv",
		Short:   "Quiver — a CLI-first multi-protocol API client (HTTP, gRPC, GraphQL)",
		Version: version(),
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

// Execute runs the root command and returns the process exit code: 0 on
// success, 1 for run failures, 2 for config errors, with the cause printed
// exactly once (Q4).
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
