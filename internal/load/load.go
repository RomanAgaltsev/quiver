package load

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/RomanAgaltsev/metronome"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/env"
	"github.com/RomanAgaltsev/quiver/internal/history"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/runner"
)

// Options is everything Execute needs. Registry, Targets, Resolved and Profile
// are required; the rest are optional.
type Options struct {
	Registry core.Registry
	Targets  []*request.Request
	Setup    []*request.Request // may be nil
	Resolved *env.Resolved
	Auth     map[string]request.AuthProfile
	History  *history.Store // may be nil; used by the setup phase only
	Profile  *Profile

	Clock            metronome.Clock // nil == metronome.SystemClock()
	Progress         io.Writer       // nil == disabled
	ProgressInterval time.Duration   // 0 == 1s
}

// The histogram range every load run records into. These are metronome's own
// NewStats defaults, spelled out here because they are not just a constructor
// argument: Evaluate has to know where the bounds are to tell a clamp at the
// top (which makes a percentile understate reality) from a clamp at the bottom
// (which cannot). Passing them explicitly keeps the two in step.
const (
	statsLow     = time.Microsecond
	statsHigh    = time.Minute
	statsSigfigs = 3
)

// ErrSetup marks a failure of the --setup chain. Callers need it to tell the
// two pre-load failures apart: a bad definition sends nothing and is exit 2,
// whereas a setup chain that ran and was refused did reach the target and is
// exit 1. Collapsing them would have exit 2 claim "nothing was sent" about a
// run that had already authenticated against a real system.
var ErrSetup = errors.New("setup chain failed")

// ValidateTargets rejects any target that cannot be meaningfully load-tested.
//
// A request declaring captures: is valid for `qv run` but not as a load target:
// across thousands of concurrent iterations there is no coherent answer to
// which response's captured value wins. Silently ignoring the block would let a
// user believe a chain is happening when it is not, so this is a hard error.
// A folder target shares one run shape, taken from the FIRST request's load:
// block, with weight the only per-file knob. A block on any later file is
// therefore ignored — and `thresholds:` among the ignored keys means a run can
// go green having asserted nothing its author asked for, which is the worst
// kind of pass. Rejecting is the same call the captures rule makes above, for
// the same reason.
func ValidateTargets(targets []*request.Request) error {
	for _, r := range targets {
		if len(r.Captures) > 0 {
			return fmt.Errorf(
				"request %q declares capture %q: captures are not supported under load "+
					"(there is no single winner across concurrent iterations) — "+
					"move the chain into a --setup folder",
				r.Name, r.Captures[0].Var)
		}
	}

	for _, r := range targets[1:] {
		if keys := r.Load.KeysBesidesWeight(); len(keys) > 0 {
			return fmt.Errorf(
				"request %q declares load: %s, but a folder target takes its run shape from "+
					"the first request's load: block and %s would be ignored — "+
					"move %s to the first request, or keep only `weight` here",
				r.Name, strings.Join(keys, ", "),
				pluralIsAre(len(keys)), strings.Join(keys, ", "))
		}
	}
	return nil
}

func pluralIsAre(n int) string {
	if n == 1 {
		return "it"
	}
	return "they"
}

// Execute runs the setup chain, resolves the targets, drives them through
// metronome, and aggregates the result.
func Execute(ctx context.Context, opts Options) (Run, error) {
	if len(opts.Targets) == 0 {
		return Run{}, fmt.Errorf("load: no target requests")
	}
	if err := ValidateTargets(opts.Targets); err != nil {
		return Run{}, err
	}

	// The variable set the targets resolve against. The setup phase adds to it.
	vars := &env.Resolved{
		Vars:    make(map[string]string, len(opts.Resolved.Vars)),
		Secrets: opts.Resolved.Secrets,
	}
	for k, v := range opts.Resolved.Vars {
		vars.Vars[k] = v
	}

	if len(opts.Setup) > 0 {
		if err := runSetup(ctx, opts, vars); err != nil {
			return Run{}, err
		}
	}

	runnerImpl, target, err := buildRunner(opts, vars)
	if err != nil {
		return Run{}, err
	}

	snap, err := drive(ctx, opts, runnerImpl)
	if err != nil {
		return Run{}, err
	}

	return Run{
		Target:   target,
		Profile:  opts.Profile,
		Snapshot: snap,
		Eval:     Evaluate(snap, opts.Profile),
	}, nil
}

// runSetup runs the auth chain once through quiver's EXISTING sequential
// runner — captures, assertions, history and ordering all behave exactly as
// they do for `qv run` — and threads its captured variables forward.
func runSetup(ctx context.Context, opts Options, vars *env.Resolved) error {
	rn := runner.New(opts.Registry, opts.History, runner.Options{})
	results := rn.RunFolder(ctx, opts.Setup, vars, opts.Auth)

	for _, res := range results {
		if res.Err != nil {
			return fmt.Errorf("%w: setup %q: %w", ErrSetup, res.Name, res.Err)
		}
		for k, v := range res.Captured {
			vars.Vars[k] = v
		}
	}
	if code := runner.ExitCode(results); code != 0 {
		return fmt.Errorf("%w: its assertions did not pass; no load was generated", ErrSetup)
	}
	return nil
}

// buildRunner resolves every target once and returns the metronome Runner —
// a single executorRunner, or a weighted Mix for a folder target.
func buildRunner(opts Options, vars *env.Resolved) (metronome.Runner, string, error) {
	weighted := make([]metronome.Weighted, 0, len(opts.Targets))
	names := make([]string, 0, len(opts.Targets))

	for _, r := range opts.Targets {
		rr, err := env.Resolve(r, vars, opts.Auth)
		if err != nil {
			return nil, "", err
		}
		exec, ok := opts.Registry[r.Protocol]
		if !ok {
			return nil, "", fmt.Errorf("load: no executor for protocol %q", r.Protocol)
		}

		var checks []request.Assertion
		if opts.Profile.Assertions {
			checks = r.Assertions
		}

		weight := 1
		if r.Load != nil && r.Load.Weight > 0 {
			weight = r.Load.Weight
		}
		weighted = append(weighted, metronome.Weighted{
			Runner: newExecutorRunner(exec, *rr, r.Name, checks, opts.Clock),
			Weight: weight,
		})
		names = append(names, describeTarget(r, rr))
	}

	if len(weighted) == 1 {
		return weighted[0].Runner, names[0], nil
	}
	return metronome.Mix(weighted...), strings.Join(names, ", "), nil
}

func describeTarget(r *request.Request, rr *core.ResolvedRequest) string {
	switch {
	case rr.HTTP != nil:
		return fmt.Sprintf("%s %s", strings.ToUpper(rr.HTTP.Method), rr.HTTP.URL)
	case rr.GRPC != nil:
		return fmt.Sprintf("gRPC %s %s", rr.GRPC.Target, rr.GRPC.Method)
	case rr.GraphQL != nil:
		return fmt.Sprintf("GraphQL %s", rr.GraphQL.URL)
	default:
		return r.Name
	}
}

// drive runs the metronome Driver and aggregates its results.
//
// The result channel is drained to completion on every path. metronome's
// contract is explicit: abandoning a live channel leaves its workers blocked on
// the send and leaks them for the lifetime of the process.
func drive(ctx context.Context, opts Options, r metronome.Runner) (metronome.Snapshot, error) {
	p := opts.Profile

	if p.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Duration)
		defer cancel()
	}

	d := metronome.Driver{
		Runner:      r,
		Rate:        p.Controller(),
		Workers:     p.Concurrency,
		MaxRequests: p.Requests,
		Pacing:      p.Pacing,
		Clock:       opts.Clock, // nil is fine: the Driver falls back to SystemClock
	}

	stats := metronome.NewStatsRange(statsLow, statsHigh, statsSigfigs)
	results := d.Run(ctx)

	if opts.Progress == nil {
		for res := range results {
			stats.Record(res)
		}
		return stats.Snapshot(), nil
	}

	interval := opts.ProgressInterval
	if interval <= 0 {
		interval = time.Second
	}
	pw := newProgressWriter(opts.Progress, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case res, ok := <-results:
			if !ok {
				return stats.Snapshot(), nil
			}
			stats.Record(res)
		case <-ticker.C:
			pw.tick(stats.Snapshot())
		}
	}
}
