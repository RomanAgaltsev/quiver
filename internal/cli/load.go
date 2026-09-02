package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RomanAgaltsev/quiver/internal/collection"
	"github.com/RomanAgaltsev/quiver/internal/load"
	"github.com/RomanAgaltsev/quiver/internal/render"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/spf13/cobra"
)

func newLoadCmd() *cobra.Command {
	var (
		setupDir         string
		rate             float64
		ramp             string
		requests         int
		concurrency      int
		pacing           string
		duration         time.Duration
		allowLag         bool
		progress         bool
		progressInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "load <request-or-folder>",
		Short: "Drive a saved request as a load test",
		Long: "Promote a saved request, or a folder of them, into a load test against the " +
			"same definition.\n\n" +
			"Exit codes: 0 ok, 1 the target failed a threshold, 2 config error, " +
			"3 the run completed but the measurement is not trustworthy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			rc, err := newRunContext(cmd, target)
			if err != nil {
				return configErr(err)
			}

			targets, err := collection.ListRequests(target)
			if err != nil {
				return configErr(err)
			}
			// Reject before resolving or authenticating: a run that will be
			// refused must not hit a real system first.
			if err := load.ValidateTargets(targets); err != nil {
				return configErr(err)
			}

			ov := load.Overrides{
				Rate: rate, Duration: duration, Requests: requests,
				Concurrency: concurrency, Pacing: pacing, AllowLag: allowLag,
			}
			if ramp != "" {
				start, end, rErr := parseRamp(ramp)
				if rErr != nil {
					return configErr(rErr)
				}
				ov.RampSet, ov.RampStart, ov.RampEnd = true, start, end
			}

			// The profile comes from the FIRST target's load: block; a folder
			// shares one run shape, with per-request `weight` the only per-file knob.
			var spec *request.LoadSpec
			if targets[0].Load != nil {
				spec = targets[0].Load
			}
			profile, err := load.ResolveProfile(spec, ov)
			if err != nil {
				return configErr(err)
			}

			var setup []*request.Request
			if setupDir != "" {
				if setup, err = collection.ListRequests(setupDir); err != nil {
					return configErr(err)
				}
			}

			opts := load.Options{
				Registry: rc.Registry, Targets: targets, Setup: setup,
				Resolved: rc.Resolved, Auth: rc.Collection.Auth,
				History: rc.History, Profile: profile,
				ProgressInterval: progressInterval,
			}
			if progress {
				opts.Progress = cmd.ErrOrStderr() // stderr keeps stdout machine-readable
			}

			run, err := load.Execute(cmd.Context(), opts)
			if err != nil {
				// A --setup failure is not a config error: those requests were
				// sent and the target refused them, so it is exit 1. Exit 2
				// promises nothing reached the system under test.
				if errors.Is(err, load.ErrSetup) {
					return runErr(err)
				}
				return configErr(err)
			}

			format, _ := cmd.Flags().GetString("output")
			switch format {
			case "", "pretty", "raw":
				format = "pretty" // load has no "raw" shape
			case "json":
			default:
				// Rejected here rather than inside WriteReport so an unsupported
				// format is exit 2 (the invocation is wrong) while a genuine
				// write failure below stays exit 1.
				return configErr(fmt.Errorf(
					"--output %q is not supported by qv load (want pretty or json)", format))
			}
			if err := load.WriteReport(cmd.OutOrStdout(), run, load.ReportOptions{
				Format:   format,
				Color:    render.ShouldColor(cmd.OutOrStdout()),
				Redactor: rc.Redactor,
			}); err != nil {
				return err
			}

			switch run.Eval.ExitCode {
			case 0:
				return nil
			case 3:
				return trustErr("the measurement is not trustworthy; see the report above")
			default:
				// `err` is nil on this path — it was checked above — so building
				// the exitError around it made Error() dereference nil, and the
				// one line the user reads on a failed gate was a formatter panic.
				return &exitError{code: run.Eval.ExitCode, err: thresholdFailure(run.Eval)}
			}
		},
	}

	cmd.Flags().StringVar(&setupDir, "setup", "", "folder to run once before the load (auth chain)")
	cmd.Flags().Float64Var(&rate, "rate", 0, "requests per second")
	cmd.Flags().StringVar(&ramp, "ramp", "", "ramp the rate, e.g. 10:100 (spans --duration)")
	cmd.Flags().IntVar(&requests, "requests", 0, "total requests to send")
	cmd.Flags().IntVar(&concurrency, "concurrency", 0, "maximum requests in flight")
	cmd.Flags().StringVar(&pacing, "pacing", "", "open (default) or closed")
	cmd.Flags().DurationVar(&duration, "duration", 0, "how long to run, e.g. 30s")
	cmd.Flags().BoolVar(&allowLag, "allow-lag", false, "downgrade a generator-lag failure to a warning")
	cmd.Flags().BoolVar(&progress, "progress", false, "print progress to stderr while running")
	cmd.Flags().DurationVar(&progressInterval, "progress-interval", time.Second, "progress tick interval")
	return cmd
}

// thresholdFailure names what the target missed, so the single line Execute
// prints on stderr says why the gate failed rather than pointing at a report
// the caller may have redirected somewhere else.
func thresholdFailure(e load.Evaluation) error {
	var failed []string
	for _, v := range e.Thresholds {
		if !v.Passed {
			failed = append(failed, fmt.Sprintf("%s: %s", v.Name, v.Detail))
		}
	}
	if len(failed) == 0 {
		return errors.New("the load run failed; see the report above")
	}
	return fmt.Errorf("threshold failed — %s", strings.Join(failed, "; "))
}

// parseRamp parses "10:100".
func parseRamp(s string) (start, end float64, err error) {
	if _, err = fmt.Sscanf(s, "%f:%f", &start, &end); err != nil {
		return 0, 0, fmt.Errorf("--ramp %q must be START:END, e.g. 10:100", s)
	}
	return start, end, nil
}
