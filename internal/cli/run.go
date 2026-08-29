package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/collection"
	"github.com/RomanAgaltsev/quiver/internal/render"
	"github.com/RomanAgaltsev/quiver/internal/runner"
)

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <request-or-folder>",
		Short: "Run a saved request file or a folder of requests",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			rc, err := newRunContext(cmd, target)
			if err != nil {
				return configErr(err)
			}

			reqs, err := collection.ListRequests(target) // ordered by `order` (Q36)
			if err != nil {
				return configErr(err)
			}

			rn := runner.New(rc.Registry, rc.History, rc.RunOpts)
			defer func() { _ = rn.Close() }() // Q42

			results := rn.RunFolder(cmd.Context(), reqs, rc.Resolved, rc.Collection.Auth)

			quiet, _ := cmd.Flags().GetBool("quiet")
			for _, res := range results {
				if err := printResult(cmd, rc, res, quiet); err != nil {
					return err
				}
			}
			if code := runner.ExitCode(results); code != 0 {
				return exitCodeErr(code)
			}
			return nil
		},
	}
}

func printResult(cmd *cobra.Command, rc *runContext, res runner.RunResult, quiet bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()

	if res.Err != nil {
		fmt.Fprintf(errOut, "%s: error: %v\n", res.Name, rc.Redactor.String(res.Err.Error()))
		return nil
	}
	// Dry-run prints the resolved request instead of a response (Q40).
	if res.Response == nil && res.Resolved != nil {
		return render.DryRun(out, res.Resolved, rc.Render)
	}
	if !quiet {
		if err := render.Render(out, res.Response, rc.Render); err != nil {
			return err
		}
		fmt.Fprintln(out)
	}
	for _, a := range res.Assertions {
		mark := "PASS"
		if !a.Passed {
			mark = "FAIL"
		}
		fmt.Fprintf(errOut, "  [%s] %s — %s\n", mark, a.Name, a.Detail)
	}
	if res.Failed {
		fmt.Fprintf(errOut, "  [FAIL] %s: non-OK response (--check-status)\n", res.Name)
	}
	return nil
}
