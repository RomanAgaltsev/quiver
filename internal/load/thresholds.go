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
	// Attempted is how many units actually reached the target: Count less the
	// ones that never found a free worker. Zero means the run measured nothing
	// about the target, however many Results it recorded.
	Attempted int64
	// AttemptedRPS is the rate the TARGET served, as distinct from the rate the
	// generator recorded. They differ by the saturated share.
	AttemptedRPS float64
	ExitCode     int
}

// Evaluate judges a finished run.
func Evaluate(snap metronome.Snapshot, p *Profile) Evaluation {
	rate, attempted := targetErrorRate(snap)
	e := Evaluation{
		TargetErrorRate: rate,
		Attempted:       attempted,
		AttemptedRPS:    attemptedRPS(snap),
	}

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
		// An empty denominator is not a clean bill of health. Reporting 0% when
		// nothing was attempted let a run in which every unit saturated satisfy
		// an error-rate threshold — "no attempts" and "no errors" must not be
		// the same number.
		switch {
		case e.Attempted == 0:
			e.Thresholds = append(e.Thresholds, Verdict{
				Name:   "error_rate",
				Passed: false,
				Detail: fmt.Sprintf(
					"no units reached the target (%d of %d saturated), so there is no error rate to judge",
					snap.Saturated, snap.Count),
			})
		default:
			e.Thresholds = append(e.Thresholds, Verdict{
				Name:   "error_rate",
				Passed: e.TargetErrorRate <= *th.ErrorRate,
				Detail: fmt.Sprintf("%.2f%% (target) vs %.2f%% allowed", e.TargetErrorRate*100, *th.ErrorRate*100),
			})
		}
	}
	if th.MinRPS != nil {
		// Judged on the rate the TARGET served, not the rate the generator
		// recorded. Snapshot.RPS is inferred from every Result including the
		// saturated ones, which never reached the target — so a fully saturated
		// run reported the offered rate as achieved. This is the same doctrine
		// targetErrorRate applies, finally applied to its sibling.
		detail := fmt.Sprintf("%.1f/s reached the target vs %.1f/s required",
			e.AttemptedRPS, *th.MinRPS)
		if snap.Saturated > 0 {
			detail += fmt.Sprintf(" (%.1f/s recorded, %d saturated)", snap.RPS, snap.Saturated)
		}
		e.Thresholds = append(e.Thresholds, Verdict{
			Name:   "min_rps",
			Passed: e.AttemptedRPS >= *th.MinRPS,
			Detail: detail,
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
// It returns the attempted count alongside the rate, because a zero denominator
// is a fact the caller must act on rather than a rate of zero: reporting 0% for
// a run in which nothing was attempted is how a fully saturated run passed an
// error-rate threshold.
func targetErrorRate(snap metronome.Snapshot) (rate float64, attempted int64) {
	attempted = snap.Count - snap.Saturated
	if attempted <= 0 {
		return 0, 0
	}
	failed := snap.Errors - snap.Saturated
	if failed <= 0 {
		return 0, attempted
	}
	return float64(failed) / float64(attempted), attempted
}

// attemptedRPS is the rate the target served.
//
// Snapshot.RPS is inferred from the timestamps of every recorded Result, and a
// saturated unit is a Result — metronome emits one immediately when no worker is
// free. So RPS describes what the generator dispatched, not what the target
// received. Scaling by the attempted share converts one into the other without
// needing the run's span, which the Snapshot does not carry.
func attemptedRPS(snap metronome.Snapshot) float64 {
	if snap.Count <= 0 {
		return 0
	}
	attempted := snap.Count - snap.Saturated
	if attempted <= 0 {
		return 0
	}
	return snap.RPS * float64(attempted) / float64(snap.Count)
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

	// Recording Results is not the same as reaching the target. A saturated unit
	// never found a free worker, so it was never sent: the percentiles, the
	// error rate and the achieved rate all describe a population smaller than
	// the run claims to have driven, and past some share they stop describing
	// the target at all.
	//
	// Nothing else catches this. schedule_lag cannot — and correctly so, since
	// OpenLoop emits a saturated unit immediately rather than delaying it, so
	// saturation produces no lag. That is precisely why saturation needs a
	// verdict of its own rather than being inferred from another.
	//
	// This is the inverse of the defect amendment A2 fixed. A2 stopped exit 3
	// firing on healthy runs; this makes it fire on a run that measured nothing,
	// which is the case exit 3 exists for.
	if share := float64(snap.Saturated) / float64(snap.Count); share > maxSaturationShare {
		out = append(out, Verdict{
			Name:   verdictSaturation,
			Passed: false,
			Detail: fmt.Sprintf(
				"%d of %d units (%.1f%%) never found a free worker and were not sent, "+
					"so the numbers describe %d requests rather than %d — "+
					"lower the rate, raise --concurrency, or use --pacing closed",
				snap.Saturated, snap.Count, share*100, snap.Count-snap.Saturated, snap.Count),
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

// verdictSaturation names the trust verdict for units that never reached the
// target. It is deliberately NOT waivable by --allow-lag: a run that mostly did
// not happen is a statement about the measurement, not about a noisy runner.
const verdictSaturation = "saturation"

// maxSaturationShare is the fraction of units that may fail to find a free
// worker before the measurement stops describing the target.
//
// A few saturated units are ordinary on any loaded machine and must not cost a
// healthy run its exit 0 — that is A2's lesson, and it is why this is a share
// rather than `Saturated > 0`. Ten percent is the point past which the excluded
// population is large enough to move a percentile.
//
// In ClosedLoop this never fires: metronome documents Saturated as always zero
// there, because a worker does not ask for its next token until the current
// unit completes.
const maxSaturationShare = 0.10

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
