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
		actual, present, err := actualValue(a, resp)
		if err != nil {
			return nil, err
		}
		passed, detail := evaluate(a, resp, actual, present)
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

// actualValue returns the value at the assertion's source and whether it is
// *present*. Presence and emptiness are different questions: the previous
// revision conflated them, so `exists` reported a present-but-empty field as
// absent — and disagreed with `capture`, which correctly used gjson's Exists on
// the same response.
func actualValue(a request.Assertion, resp *core.Response) (val string, present bool, err error) {
	switch a.From {
	case "status":
		return strconv.Itoa(resp.Status), true, nil
	case "header":
		v := resp.HeaderGet(a.Path)
		return v, v != "", nil
	case "body":
		r := gjson.GetBytes(resp.Body, a.Path)
		return r.String(), r.Exists(), nil
	default:
		// Unreachable in practice: request.Validate rejects unknown sources at
		// load time so this is a config error (exit 2), not an assertion failure.
		return "", false, fmt.Errorf("assertion: unknown source %q", a.From)
	}
}

func evaluate(a request.Assertion, resp *core.Response, actual string, present bool) (bool, string) {
	switch a.Op {
	case "eq":
		if matchesStatus(a, resp, actual) {
			return true, fmt.Sprintf("got %q want %q", actual, a.Value)
		}
		return actual == a.Value, fmt.Sprintf("got %q want %q", actual, a.Value)
	case "ne":
		return actual != a.Value, fmt.Sprintf("got %q", actual)
	case "contains":
		return strings.Contains(actual, a.Value), fmt.Sprintf("%q does not contain %q", actual, a.Value)
	case "matches":
		re, err := regexp.Compile(a.Value) // Validate already compiled this successfully
		if err != nil {
			return false, fmt.Sprintf("invalid regex %q", a.Value)
		}
		return re.MatchString(actual), fmt.Sprintf("%q does not match %q", actual, a.Value)
	case "exists":
		return present, fmt.Sprintf("present=%t", present)
	case "not_exists":
		return !present, fmt.Sprintf("present=%t", present)
	default:
		// Also unreachable: Validate rejects unknown ops (Q15).
		return false, fmt.Sprintf("unknown op %q", a.Op)
	}
}

// matchesStatus lets a gRPC status assertion be written by code name — value:
// "OK" or "NOT_FOUND" — as well as by number. value: "0" for success is
// unreadable next to the HTTP habit of value: "200".
func matchesStatus(a request.Assertion, resp *core.Response, actual string) bool {
	if a.From != "status" || resp.Protocol != request.ProtocolGRPC {
		return false
	}
	return strings.EqualFold(a.Value, resp.StatusText) && actual != a.Value
}
