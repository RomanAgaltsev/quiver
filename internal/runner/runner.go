// Package runner orchestrates resolve -> execute -> capture -> assert -> record.
package runner

import (
	"context"
	"fmt"
	"io"
	"time"

	asrt "github.com/RomanAgaltsev/quiver/internal/assert"
	"github.com/RomanAgaltsev/quiver/internal/capture"
	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/env"
	"github.com/RomanAgaltsev/quiver/internal/history"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// RunResult is the outcome of one request in a folder run: what ran, what the
// response was, what it captured, which assertions passed. Err is set for
// resolve/transport failures; Failed records a non-OK response under
// --check-status without an Err.
type RunResult struct {
	Name       string
	Path       string
	Resolved   *core.ResolvedRequest // set on the dry-run path
	Response   *core.Response
	Captured   map[string]string
	Assertions []asrt.Result
	Err        error
	// Failed records a non-OK response under --check-status, which is a run
	// failure without being an Err (the request itself succeeded).
	Failed bool
}

// Options configures run policy.
type Options struct {
	FailOnError bool              // --check-status / collection fail_on_error
	DryRun      bool              // --dry-run
	Env         string            // recorded in history for replay
	Overrides   map[string]string // --var; captures must not shadow these
	// Warn receives non-fatal diagnostics (a history write that failed). nil
	// discards them; the CLI passes stderr.
	Warn io.Writer
}

// Runner executes requests through the registry, applying captures,
// assertions, and history recording. It is safe to reuse across a folder run.
type Runner struct {
	reg  core.Registry
	hist *history.Store // may be nil (history disabled)
	opts Options
}

// New builds a Runner. hist may be nil to disable recording.
func New(reg core.Registry, hist *history.Store, opts Options) *Runner {
	return &Runner{reg: reg, hist: hist, opts: opts}
}

// Close releases executor resources (gRPC connections).
func (rn *Runner) Close() error { return rn.reg.Close() }

// RunRequest resolves and executes a single request.
func (rn *Runner) RunRequest(ctx context.Context, r *request.Request, res *env.Resolved, auth map[string]request.AuthProfile) RunResult {
	out := RunResult{Name: r.Name, Path: r.Path}

	// env.Resolve returns core.ConfigError for everything it rejects — an
	// unresolved variable, an unknown auth profile, an unreadable body_file — so
	// ExitCode can map those to 2 rather than collapsing them into a run failure.
	rr, err := env.Resolve(r, res, auth)
	if err != nil {
		out.Err = err
		return out
	}
	out.Resolved = rr

	// --dry-run stops here: resolution is the whole point, nothing is sent.
	if rn.opts.DryRun {
		return out
	}

	exec, ok := rn.reg[r.Protocol]
	if !ok {
		out.Err = core.NewConfigError(fmt.Errorf("runner: no executor for protocol %q", r.Protocol))
		return out
	}
	resp, err := exec.Execute(ctx, *rr)
	if err != nil {
		out.Err = err
		return out
	}
	out.Response = resp

	// Record as soon as a response exists. Capture and assertion failures
	// must not be the reason a request goes missing from history.
	rn.record(r, resp)

	if rn.opts.FailOnError && !resp.OK {
		out.Failed = true
	}
	// A capture path that does not resolve is a definition error too: the file is
	// wrong, not the server.
	if out.Captured, err = capture.Apply(r.Captures, resp); err != nil {
		out.Err = core.NewConfigError(err)
		return out
	}
	if out.Assertions, err = asrt.Run(r.Assertions, resp); err != nil {
		out.Err = err
		return out
	}
	return out
}

// RunFolder runs requests in order, threading captured vars forward.
func (rn *Runner) RunFolder(ctx context.Context, requests []*request.Request, res *env.Resolved, auth map[string]request.AuthProfile) []RunResult {
	merged := &env.Resolved{
		Vars:     make(map[string]string, len(res.Vars)),
		Secrets:  res.Secrets,
		Redactor: res.Redactor, // shared, so a secret found mid-run redacts at once
	}
	for k, v := range res.Vars {
		merged.Vars[k] = v
	}

	results := make([]RunResult, 0, len(requests))
	for _, r := range requests {
		out := rn.RunRequest(ctx, r, merged, auth)
		for k, v := range out.Captured {
			// A --var override wins. It is the user's debugging tool, so a
			// capture of the same name must not silently take precedence.
			if _, overridden := rn.opts.Overrides[k]; overridden {
				continue
			}
			merged.Vars[k] = v // captures flow forward to subsequent requests
		}
		results = append(results, out)
		if out.Err != nil {
			break // stop the chain on the first hard error
		}
	}
	// A secret resolved inline during request expansion (a {{env:NAME}} written
	// in the request file itself) is discovered here, not in MergeVars, so hand
	// it back to the caller's set — the redactor is built from it.
	res.Secrets = merged.Secrets
	return results
}

func (rn *Runner) record(r *request.Request, resp *core.Response) {
	if rn.hist == nil {
		return
	}
	err := rn.hist.Append(history.Record{
		ID:       history.NewID(),
		Time:     time.Now(),
		Name:     r.Name,
		Protocol: string(r.Protocol),
		Status:   resp.Status,
		OK:       resp.OK,
		Duration: resp.Duration.String(),
		Path:     r.Path, // Q6: what `qv history replay` re-runs
		Env:      rn.opts.Env,
		Vars:     rn.opts.Overrides,
	})
	// Never fail the run for this — the request itself succeeded — but never hide
	// it either: a history that has silently stopped recording is worse than none.
	if err != nil && rn.opts.Warn != nil {
		_, _ = fmt.Fprintf(rn.opts.Warn, "qv: warning: history not recorded: %v\n", err)
	}
}

// ExitCode returns the highest applicable process exit code: 2 when any failure
// was a configuration error, 1 for a transport failure, a failed assertion or a
// non-OK response under --check-status, and 0 otherwise.
//
// A config error outranks a run failure because it means the run never happened,
// and CI has to tell "the API is broken" apart from "the YAML is wrong".
//
// Note the documented default: a non-2xx response with no assertions declared
// exits 0, because explicit assertions are the contract. --check-status sets
// Options.FailOnError and flips that.
func ExitCode(results []RunResult) int {
	code := 0
	for _, res := range results {
		switch {
		case res.Err != nil && core.IsConfigError(res.Err):
			return 2
		case res.Err != nil, res.Failed, !asrt.AllPassed(res.Assertions):
			code = 1
		}
	}
	return code
}
