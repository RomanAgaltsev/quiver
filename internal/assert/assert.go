// Package assert runs declarative checks over a normalized response.
package assert

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Result is the outcome of one assertion: Passed plus a human-readable Detail
// that printResult shows verbatim next to the PASS/FAIL mark.
type Result struct {
	Name   string
	Passed bool
	Detail string
}

// Run evaluates each assertion against the response, in file order. A
// malformed assertion (unknown operator, bad regex) is an error, not a fail —
// the same config-vs-run distinction the rest of the CLI makes.
func Run(assertions []request.Assertion, resp *core.Response) ([]Result, error) {
	results := make([]Result, 0, len(assertions))
	for i, a := range assertions {
		vals, present, err := actualValues(a, resp)
		if err != nil {
			return nil, err
		}
		// Compiled once per assertion rather than once per evaluation. Validate has
		// already proved it compiles, so a failure here is a genuine config error.
		var re *regexp.Regexp
		if a.Op == "matches" {
			if re, err = regexp.Compile(a.Operand()); err != nil {
				return nil, core.NewConfigError(
					fmt.Errorf("assertion %d: invalid regex %q: %w", i, a.Operand(), err))
			}
		}
		passed, detail := evaluate(a, vals, present, re)
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("assertion[%d]", i)
		}
		results = append(results, Result{Name: name, Passed: passed, Detail: detail})
	}
	return results, nil
}

// AllPassed reports whether every result passed; an empty set passes. It is
// the single place that turns assertion results into an exit-code decision.
func AllPassed(rs []Result) bool {
	for _, r := range rs {
		if !r.Passed {
			return false
		}
	}
	return true
}

// actualValues returns every representation of the source value that an
// assertion may legitimately be written against, plus whether the source is
// *present*.
//
// For a gRPC status that is the numeric code and its name, because spec §8
// accepts both — and it has to hold for every operator, not only `eq`. Wiring
// the name into `eq` alone meant `op: ne, value: "OK"` compared "0" against "OK"
// and silently always passed, asserting the opposite of what was written.
//
// Presence and emptiness are different questions: conflating them made `exists`
// report a present-but-empty field as absent, disagreeing with `capture`.
func actualValues(a request.Assertion, resp *core.Response) (vals []string, present bool, err error) {
	switch a.From {
	case "status":
		vals = []string{strconv.Itoa(resp.Status)}
		if resp.Protocol == request.ProtocolGRPC && resp.StatusText != "" {
			vals = append(vals, resp.StatusText)
		}
		return vals, true, nil
	case "header":
		v, ok := resp.HeaderPresent(a.Path)
		return []string{v}, ok, nil
	case "body":
		r := gjson.GetBytes(resp.Body, a.Path)
		return []string{r.String()}, r.Exists(), nil
	default:
		// Unreachable in practice: request.Validate rejects unknown sources at
		// load time so this is a config error (exit 2), not an assertion failure.
		return nil, false, core.NewConfigError(fmt.Errorf("assertion: unknown source %q", a.From))
	}
}

func evaluate(a request.Assertion, vals []string, present bool, re *regexp.Regexp) (bool, string) {
	want := a.Operand()
	primary := vals[0]

	// any: the assertion holds if *some* representation matches (a gRPC status is
	// both "0" and "OK"). all: it must hold for every one, which is what makes a
	// negative operator correct.
	matchAny := func(f func(string) bool) bool {
		for _, v := range vals {
			if f(v) {
				return true
			}
		}
		return false
	}
	matchAll := func(f func(string) bool) bool {
		for _, v := range vals {
			if !f(v) {
				return false
			}
		}
		return true
	}

	switch a.Op {
	case "eq":
		return matchAny(func(v string) bool { return v == want }),
			fmt.Sprintf("got %q want %q", primary, want)
	case "ne":
		return matchAll(func(v string) bool { return v != want }),
			fmt.Sprintf("got %q", primary)
	case "contains":
		return matchAny(func(v string) bool { return strings.Contains(v, want) }),
			fmt.Sprintf("%q does not contain %q", primary, want)
	case "matches":
		return matchAny(re.MatchString), fmt.Sprintf("%q does not match %q", primary, want)
	case "exists":
		return present, fmt.Sprintf("present=%t", present)
	case "not_exists":
		return !present, fmt.Sprintf("present=%t", present)
	default:
		// Also unreachable: Validate rejects unknown ops (Q15).
		return false, fmt.Sprintf("unknown op %q", a.Op)
	}
}
