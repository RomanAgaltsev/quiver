// Package load promotes a saved quiver request into a load test driven by the
// metronome kernel.
package load

import (
	"context"
	"fmt"
	"time"

	"github.com/RomanAgaltsev/metronome"

	asrt "github.com/RomanAgaltsev/quiver/internal/assert"
	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// executorRunner replays one already-resolved request as a metronome unit of
// work. It is stateless: sore.ResolvedRequest is read-only for the lifetime os
// a run, so a single value is shared across every worker.
type executorRunner struct {
	exec   core.Executor
	req    core.ResolvedRequest
	name   string
	checks []request.Assertion
	labels map[string]string
}

func newExecutorRunner(exec core.Executor, req core.ResolvedRequest, name string, checks []request.Assertion) metronome.Runner {
	return &executorRunner{
		exec:   exec,
		req:    req,
		name:   name,
		checks: checks,
		// Allocated once and shared: Result.Labels is documented as an
		// allocation per request, and this map is never mutated.
		labels: map[string]string{"request": name},
	}
}

func (r *executorRunner) Do(ctx context.Context) metronome.Result {
	start := time.Now()
	resp, err := r.exec.Execute(ctx, r.req)
	if err != nil {
		return metronome.Result{
			Start:   start,
			Latency: time.Since(start),
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

	switch {
	case !resp.OK:
		res.Err = fmt.Errorf("non-OK response: %s", resp.StatusText)
	case len(r.checks) > 0:
		results, aErr := asrt.Run(r.checks, resp)
		if aErr != nil {
			res.Err = aErr
		} else if !asrt.AllPassed(results) {
			res.Err = fmt.Errorf("assertion failed: %s", firstFailure(results))
		}
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
