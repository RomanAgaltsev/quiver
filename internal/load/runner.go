// Package load promotes a saved quiver request into a load test driven by the
// metronome kernel.
package load

import (
	"context"
	"fmt"

	"github.com/RomanAgaltsev/metronome"

	asrt "github.com/RomanAgaltsev/quiver/internal/assert"
	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// executorRunner replays one already-resolved request as a metronome unit of
// work. It is stateless: core.ResolvedRequest is read-only for the lifetime of
// a run, so a single value is shared across every worker.
type executorRunner struct {
	exec   core.Executor
	req    core.ResolvedRequest
	name   string
	checks []request.Assertion
	labels map[string]string
	clock  metronome.Clock
}

// newExecutorRunner builds the unit of work. A nil clock means the system clock,
// matching Options.Clock.
func newExecutorRunner(
	exec core.Executor,
	req core.ResolvedRequest,
	name string,
	checks []request.Assertion,
	clock metronome.Clock,
) metronome.Runner {
	if clock == nil {
		clock = metronome.SystemClock()
	}
	return &executorRunner{
		exec:   exec,
		req:    req,
		name:   name,
		checks: checks,
		// Allocated once and shared: Result.Labels is documented as an
		// allocation per request, and this map is never mutated.
		labels: map[string]string{"request": name},
		clock:  clock,
	}
}

func (r *executorRunner) Do(ctx context.Context) metronome.Result {
	// From the injected clock, not the wall clock: metronome infers RPS and
	// Throughput from these stamps, so a ManualClock run would otherwise derive
	// its rate figures from real elapsed time.
	start := r.clock.Now()
	resp, err := r.exec.Execute(ctx, r.req)
	if err != nil {
		return metronome.Result{
			Start:   start,
			Latency: r.clock.Now().Sub(start),
			Err:     err,
			Labels:  r.labels,
		}
	}

	res := metronome.Result{
		Start: start,
		// The executor already measured the transport precisely; using its
		// number keeps quiver's own dispatch overhead out of the percentiles.
		Latency: resp.Duration,
		Code:    resp.StatusText,
		Bytes:   int64(len(resp.Body)),
		Labels:  r.labels,
	}

	// Declared assertions are the contract and decide the outcome on their own,
	// including for a non-OK response. Checking !resp.OK first meant an
	// error-path load test — a request asserting `status eq 404` — reported a
	// 100% error rate, and its assertion was never evaluated at all. It also
	// made the same file mean different things under the two commands: `qv run`
	// fails on non-OK only under --check-status and otherwise lets assertions
	// decide, which is the behaviour this phase promised to preserve.
	//
	// With nothing declared there is no other success signal, so a non-OK
	// response remains the error — the common case, unchanged.
	switch {
	case len(r.checks) > 0:
		results, aErr := asrt.Run(r.checks, resp)
		if aErr != nil {
			res.Err = aErr
		} else if !asrt.AllPassed(results) {
			res.Err = fmt.Errorf("assertion failed: %s", firstFailure(results))
		}
	case !resp.OK:
		res.Err = fmt.Errorf("non-OK response: %s", resp.StatusText)
	}
	return res
}

// firstFailure names the first failed assertion for the Result's error.
func firstFailure(results []asrt.Result) string {
	for _, r := range results {
		if !r.Passed {
			return fmt.Sprintf("%s (%s)", r.Name, r.Detail)
		}
	}
	return "unknown"
}
