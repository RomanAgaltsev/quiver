package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/collection"
	"github.com/RomanAgaltsev/quiver/internal/runner"
)

func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "history", Short: "Inspect request history"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List recent requests",
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			if rc.History == nil { // --dry-run opens no store
				return configErr(fmt.Errorf("history is unavailable under --dry-run"))
			}
			recs, err := rc.History.List()
			if err != nil {
				return err
			}
			for _, r := range recs {
				status := fmt.Sprintf("%d", r.Status)
				if !r.OK {
					status += " !"
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %-8s %-20s %-6s %s\n",
					r.ID, r.Protocol, r.Name, status, r.Duration); err != nil {
					return err
				}
			}
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "replay <id>",
		Short: "Re-run a request from history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := newRunContext(cmd, "")
			if err != nil {
				return configErr(err)
			}
			if rc.History == nil { // --dry-run opens no store
				return configErr(fmt.Errorf("history is unavailable under --dry-run"))
			}
			rec, err := rc.History.Get(args[0])
			if err != nil {
				return classify(err)
			}
			// This is only possible because Record now stores the source path.
			// Re-resolve from disk rather than replaying a frozen request,
			// so a replay reflects the current file and environment.
			if rec.Path == "" {
				return configErr(fmt.Errorf("history record %s has no source path", rec.ID))
			}
			reqs, err := collection.ListRequests(rec.Path)
			if err != nil {
				return classify(err)
			}
			rn := runner.New(rc.Registry, rc.History, rc.RunOpts)
			defer func() { _ = rn.Close() }()

			results := rn.RunFolder(cmd.Context(), reqs, rc.Resolved, rc.Collection.Auth)
			quiet, _ := cmd.Flags().GetBool("quiet")
			for _, res := range results {
				if err := printResult(cmd, rc, res, quiet); err != nil {
					return err
				}
			}
			if code := runner.ExitCode(results); code != 0 {
				return runFailure(code, results)
			}
			return nil
		},
	})
	return cmd
}
