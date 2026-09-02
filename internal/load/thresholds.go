package load

import (
	"fmt"
	"time"

	"github.com/RomanAgaltsev/metronome"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// Verdict is one pass/fail judgement with a human-readable detail.
type Verdict struct {
	Name   string
	Passed bool
	Detail string
}

// Evaluation is the whole outcome of a load run: what the target was judged on,
// whether the measurement itself can be believed, and the resulting exit code.
type Evaluation struct {
	// Thresholds judge the TARGET. A failure here is exit 1.
	Thresholds []Verdict
	// Trust judges the MEASUREMENT. A failure here is exit 3, because a verdict
	// derived from numbers that do not describe the target is worthless.
	Trust []Verdict
	// TargetErrorRate excludes saturation from both terms — see below.
	TargetErrorRate float64
	ExitCode        int
}

// Evaluate judges a finished run.
func Evaluate(snap metronome.Snapshot, p *Profile) Evaluation {
	e := Evaluation{TargetErrorRate: targetErrorRate(snap)}

	th := p.Thresholds
	if d := th.P50.Duration(); d > 0 {
		e.Thresholds = append(e.Thresholds, latencyVerdict("p50", snap.P50, d))
	}
	if d := th.P95.Duration(); d > 0 {
		e.Thresholds = append(e.Thresholds, latencyVerdict("p95", snap.P95, d))
	}
	if d := th.P99.Duration(); d > 0 {
		e.Thresholds = append(e.Thresholds, latencyVerdict("p99", snap.P99, d))
	}
	if d := th.CorrectedP50.Duration(); d > 0 {
		e.Thresholds = append(e.Thresholds, latencyVerdict("corrected_p50", snap.CorrectedP50, d))
	}
	if d := th.CorrectedP95.Duration(); d > 0 {
		e.Thresholds = append(e.Thresholds, latencyVerdict("corrected_p95", snap.CorrectedP95, d))
	}
	if d := th.CorrectedP99.Duration(); d > 0 {
		e.Thresholds = append(e.Thresholds, latencyVerdict("corrected_p99", snap.CorrectedP99, d))
	}
	if th.ErrorRate != nil {
		e.Thresholds = append(e.Thresholds, Verdict{
			Name:   "error_rate",
			Passed: e.TargetErrorRate <= *th.ErrorRate,
			Detail: fmt.Sprintf("%.2f%% (target) vs %.2f%% allowed", e.TargetErrorRate*100, *th.ErrorRate*100),
		})
	}
	if th.MinRPS != nil {
		e.Thresholds = append(e.Thresholds, Verdict{
			Name:   "min_rps",
			Passed: snap.RPS >= *th.MinRPS,
			Detail: fmt.Sprintf("%.1f/s achieved vs %.1f/s required", snap.RPS, *th.MinRPS),
		})
	}

	e.Trust = trustVerdicts(snap, p)

	switch {
	// Trust wins: if the numbers cannot be believed, neither can a verdict
	// derived from them.
	case anyFailed(e.Trust) && !p.AllowLag:
		e.ExitCode = 3
	case anyFailed(e.Thresholds):
		e.ExitCode = 1
	default:
		e.ExitCode = 0
	}
	return e
}

// targetErrorRate is the error rate attributable to the TARGET.
//
// metronome deliberately counts Saturated inside Errors and ErrorRate so that
// saturation is visible rather than appearing as silent rate sag. But a
// saturated unit never reached the target — no worker was free at its scheduled
// time — so it belongs in neither the numerator nor the denominator here.
// Comparing a declared error_rate against Snapshot.ErrorRate would blame the
// target for the generator running out of workers.
func targetErrorRate(snap metronome.Snapshot) float64 {
	attempted := snap.Count - snap.Saturated
	if attempted <= 0 {
		return 0
	}
	failed := snap.Errors - snap.Saturated
	if failed <= 0 {
		return 0
	}
	return float64(failed) / float64(attempted)
}

// trustVerdicts judge whether the measurement describes the target at all.
func trustVerdicts(snap metronome.Snapshot, p *Profile) []Verdict {
	var out []Verdict

	if snap.Count == 0 {
		return append(out, Verdict{
			Name:   "results",
			Passed: false,
			Detail: "no results were recorded — the run measured nothing",
		})
	}

	// Lag WITHOUT saturation is metronome failing to keep its own schedule, so
	// the latency numbers describe quiver rather than the target. Lag WITH
	// saturation means the target could not keep up, which is a target result
	// the thresholds already judge.
	budget := p.LagBudget()
	if snap.MaxScheduleLag > budget && snap.Saturated == 0 {
		out = append(out, Verdict{
			Name:   "schedule_lag",
			Passed: false,
			Detail: fmt.Sprintf(
				"generator fell %s behind its schedule (budget %s) with no saturation — "+
					"lower the rate, raise --concurrency, or use --pacing closed",
				snap.MaxScheduleLag.Round(time.Millisecond), budget),
		})
	}

	// A clamped histogram understates the percentiles, so asserting on them is
	// asserting on a number that is not real.
	if snap.Clamped > 0 && hasRawLatencyThreshold(p.Thresholds) {
		out = append(out, Verdict{
			Name:   "histogram_range",
			Passed: false,
			Detail: fmt.Sprintf(
				"%d result(s) fell outside the histogram range, so p50/p95/p99 understate reality",
				snap.Clamped),
		})
	}
	if snap.CorrectedClamped > 0 && hasCorrectedLatencyThreshold(p.Thresholds) {
		out = append(out, Verdict{
			Name:   "corrected_histogram_range",
			Passed: false,
			Detail: fmt.Sprintf(
				"%d corrected result(s) were clamped, so corrected_p* understate reality",
				snap.CorrectedClamped),
		})
	}
	return out
}

func hasRawLatencyThreshold(th request.Thresholds) bool {
	return th.P50.Duration() > 0 || th.P95.Duration() > 0 || th.P99.Duration() > 0
}

func hasCorrectedLatencyThreshold(th request.Thresholds) bool {
	return th.CorrectedP50.Duration() > 0 || th.CorrectedP95.Duration() > 0 || th.CorrectedP99.Duration() > 0
}

func latencyVerdict(name string, got, want time.Duration) Verdict {
	return Verdict{
		Name:   name,
		Passed: got <= want,
		Detail: fmt.Sprintf("%s vs %s allowed", got.Round(time.Microsecond), want),
	}
}

func anyFailed(vs []Verdict) bool {
	for _, v := range vs {
		if !v.Passed {
			return true
		}
	}
	return false
}
