package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// There is deliberately no `env use` (spec §6): it needs persisted selection
// state plus a precedence rule against --env, and staying stateless is what
// keeps `qv` reproducible in CI.
func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Inspect environments and resolved variables",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List available environments",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Reads <root>/environments via newRunContext, not the process CWD,
			// so `qv env list` behaves the same from any directory (Q14).
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			entries, err := os.ReadDir(filepath.Join(rc.Collection.Root, "environments"))
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			// os.ReadDir is already sorted by name, so output order is stable.
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
					continue
				}
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSuffix(e.Name(), ".yaml")); err != nil {
					return err
				}
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Show the resolved variables for --env, redacted",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// newRunContext already merged defaults + --env + --var and built the
			// redactor from the resolved secrets (Q5). Printing rc.Resolved
			// through it is the cheapest end-to-end demonstration of the secret
			// pipeline: without the redactor this command would be a leak.
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			keys := make([]string, 0, len(rc.Resolved.Vars))
			for k := range rc.Resolved.Vars {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", k, rc.Redactor.String(rc.Resolved.Vars[k])); err != nil {
					return err
				}
			}
			return nil
		},
	})

	return cmd
}
