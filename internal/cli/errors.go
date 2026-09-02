package cli

import (
	"errors"
	"fmt"

	asrt "github.com/RomanAgaltsev/quiver/internal/assert"
	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/runner"
)

// exitError carries a specific process exit code and the cause, up to Execute.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
func (e *exitError) Code() int     { return e.code }

// configErr marks a user/config error so it maps to exit code 2.
func configErr(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: 2, err: err}
}

// runErr marks a transport or assertion failure so it maps to exit code 1.
func runErr(err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: 1, err: err}
}

// classify maps an error to the exit code its *kind* deserves rather than to a
// flat 1. A core.ConfigError means the definition is wrong and nothing was sent,
// which spec §8 maps to 2; anything else is a genuine run failure.
func classify(err error) error {
	if err == nil {
		return nil
	}
	if core.IsConfigError(err) {
		return configErr(err)
	}
	return runErr(err)
}

// runFailure summarises why a run failed, for the single line Execute prints.
//
// It must never restate the exit code as a count: `code` is 1 or 2, so the
// previous "%d assertion(s) or request(s) failed" printed "1 assertion(s)
// failed" for every failure, including runs with no assertions at all.
func runFailure(code int, results []runner.RunResult) error {
	var errored, failed int
	var first string
	note := func(s string) {
		if first == "" {
			first = s
		}
	}
	for _, r := range results {
		switch {
		case r.Err != nil:
			errored++
			note(r.Err.Error())
		case r.Failed:
			failed++
			note(fmt.Sprintf("%s: non-OK response (--check-status)", r.Name))
		case !asrt.AllPassed(r.Assertions):
			failed++
			note(fmt.Sprintf("%s: %s", r.Name, firstFailedAssertion(r.Assertions)))
		}
	}
	return &exitError{code: code, err: fmt.Errorf(
		"%d request(s) errored, %d failed — first: %s", errored, failed, first)}
}

func firstFailedAssertion(rs []asrt.Result) string {
	for _, r := range rs {
		if !r.Passed {
			return fmt.Sprintf("assertion %q failed (%s)", r.Name, r.Detail)
		}
	}
	return "assertions failed"
}

// trustErr marks a run whose measurement cannot be believed — the generator did
// not keep its own schedule, or the histogram clamped. It is deliberately NOT
// exit 1: "the target is too slow" and "quiver could not generate the load" are
// different failures with different fixes, and collapsing them would discard the
// signal metronome exists to surface.
func trustErr(msg string) error {
	return &exitError{code: 3, err: errors.New(msg)}
}
