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
	case anyFatalTrustFailure(e.Trust, p.AllowLag):
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

	// Whether schedule lag indicts the generator or the target is decided by the
	// pacing mode, not by the saturation count.
	//
	// In OpenLoop a unit that finds no free worker is emitted immediately as an
	// ErrSaturated Result — metronome's dispatcher never delays it. A busy
	// target therefore cannot produce schedule lag at all: lag there is the
	// pacer failing to keep the schedule it offered, which is quiver's problem
	// whatever Saturated says. metronome's open-loop dispatcher has a ceiling of
	// a few thousand rps, and a load test can walk straight into it.
	//
	// In ClosedLoop the opposite holds by construction: a worker does not ask
	// for its next token until the current unit completes, so a slow target IS
	// the lag, absorbed as rate sag — a target result the thresholds already
	// judge through min_rps and the corrected percentiles.
	//
	// Keying this on `Saturated == 0` got it backwards at both ends. It silenced
	// the check in OpenLoop, where lag means the generator, as soon as a single
	// unit saturated — 28 saturated out of 10,504 was enough to hide a 2.9s lag
	// against a 25ms budget. And it armed the check in ClosedLoop, where
	// Saturated is documented to be always zero and lag means the target.
	budget := p.LagBudget()
	if p.Pacing == metronome.OpenLoop && snap.MaxScheduleLag > budget {
		out = append(out, Verdict{
			Name:   verdictScheduleLag,
			Passed: false,
			Detail: fmt.Sprintf(
				"generator fell %s behind its schedule (budget %s) — "+
					"lower the rate, raise --concurrency, or use --pacing closed",
				snap.MaxScheduleLag.Round(time.Millisecond), budget),
		})
	}

	// A clamped histogram understates the percentiles ONLY when it clamped at
	// the TOP. metronome counts both ends in one Clamped counter, but the two
	// ends are not symmetric: a latency below the floor is recorded AS the
	// floor, which rounds a percentile up and so can never let a "must be under
	// X" threshold pass when it should have failed. Clamping at the ceiling is
	// the dangerous one, because then p99 reads 1m for a request that took an
	// hour.
	//
	// The distinction is not academic. Low-side clamping is the normal case for
	// a fast local target — and unavoidable on a host whose monotonic clock is
	// coarser than a microsecond, such as Windows at ~0.5ms, where nearly every
	// loopback result measures zero. Treating that as a trust failure made
	// exit 3 the routine outcome of a perfectly healthy run.
	//
	// Snapshot.Max is documented to be the true maximum regardless of clamping,
	// so it is the honest test for whether the ceiling was ever reached.
	if snap.Clamped > 0 && snap.Max > statsHigh && hasRawLatencyThreshold(p.Thresholds) {
		out = append(out, Verdict{
			Name:   "histogram_range",
			Passed: false,
			Detail: fmt.Sprintf(
				"%d result(s) exceeded the %s histogram ceiling (max was %s), "+
					"so p50/p95/p99 understate reality",
				snap.Clamped, statsHigh, snap.Max.Round(time.Millisecond)),
		})
	}
	// A corrected value is a latency plus its queueing delay, so Max plus the
	// largest observed lag bounds every one of them: under that, nothing can
	// have clamped at the ceiling.
	if snap.CorrectedClamped > 0 && snap.Max+snap.MaxScheduleLag > statsHigh &&
		hasCorrectedLatencyThreshold(p.Thresholds) {
		out = append(out, Verdict{
			Name:   "corrected_histogram_range",
			Passed: false,
			Detail: fmt.Sprintf(
				"%d corrected result(s) exceeded the %s histogram ceiling, "+
					"so corrected_p* understate reality",
				snap.CorrectedClamped, statsHigh),
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

// verdictScheduleLag names the one trust verdict --allow-lag may waive.
const verdictScheduleLag = "schedule_lag"

// anyFatalTrustFailure reports whether a trust verdict should force exit 3.
//
// --allow-lag waives ONLY schedule_lag, which is what its help text promises
// and the only failure a noisy CI runner can cause on its own. A clamped
// histogram or a run that recorded nothing are statements about the numbers
// themselves — waiving those would let --allow-lag turn "these percentiles are
// not real" into a silent exit 0, which is precisely the signal this package
// exists to preserve.
func anyFatalTrustFailure(vs []Verdict, allowLag bool) bool {
	for _, v := range vs {
		if v.Passed {
			continue
		}
		if allowLag && v.Name == verdictScheduleLag {
			continue
		}
		return true
	}
	return false
}

func anyFailed(vs []Verdict) bool {
	for _, v := range vs {
		if !v.Passed {
			return true
		}
	}
	return false
}
