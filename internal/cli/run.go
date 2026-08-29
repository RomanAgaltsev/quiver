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
		if _, err := fmt.Fprintf(errOut, "%s: error: %v\n", res.Name, rc.Redactor.String(res.Err.Error())); err != nil {
			return err
		}
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
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	for _, a := range res.Assertions {
		mark := "PASS"
		if !a.Passed {
			mark = "FAIL"
		}
		if _, err := fmt.Fprintf(errOut, "  [%s] %s — %s\n", mark, a.Name, a.Detail); err != nil {
			return err
		}
	}
	if res.Failed {
		if _, err := fmt.Fprintf(errOut, "  [FAIL] %s: non-OK response (--check-status)\n", res.Name); err != nil {
			return err
		}
	}
	return nil
}
