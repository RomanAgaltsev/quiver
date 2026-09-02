package load

import (
	"testing"
	"time"

	"github.com/RomanAgaltsev/metronome"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/stretchr/testify/require"
)

func f64(v float64) *float64 { return &v }

func profileWith(th request.Thresholds) *Profile {
	return &Profile{Rate: 50, Duration: time.Second, Pacing: metronome.OpenLoop, Thresholds: th}
}

// With nothing declared a run exits 0 — exploration must not fail, matching the
// MVP's stance that explicit assertions are the contract.
func TestEvaluateNoThresholdsPasses(t *testing.T) {
	snap := metronome.Snapshot{Count: 100, Errors: 50, P99: time.Minute}
	e := Evaluate(snap, profileWith(request.Thresholds{}))
	require.Equal(t, 0, e.ExitCode)
	require.Empty(t, e.Thresholds)
	require.Empty(t, e.Trust)
}

func TestEvaluateTargetThresholds(t *testing.T) {
	snap := metronome.Snapshot{
		Count: 1000, Errors: 2, RPS: 49.8,
		P99: 48 * time.Millisecond, CorrectedP99: 52 * time.Millisecond,
	}
	e := Evaluate(snap, profileWith(request.Thresholds{
		P99:          secs(250 * time.Millisecond),
		CorrectedP99: secs(500 * time.Millisecond),
		ErrorRate:    f64(0.01),
		MinRPS:       f64(45),
	}))
	require.Equal(t, 0, e.ExitCode)
	require.Len(t, e.Thresholds, 4)
	for _, v := range e.Thresholds {
		require.True(t, v.Passed, v.Name)
	}
}

func TestEvaluateFailingThresholdExitsOne(t *testing.T) {
	snap := metronome.Snapshot{Count: 1000, P99: 900 * time.Millisecond}
	e := Evaluate(snap, profileWith(request.Thresholds{P99: secs(250 * time.Millisecond)}))
	require.Equal(t, 1, e.ExitCode)
	require.False(t, e.Thresholds[0].Passed)
	require.Contains(t, e.Thresholds[0].Detail, "900ms")
}

func TestEvaluateMinRPSIsALowerBound(t *testing.T) {
	e := Evaluate(metronome.Snapshot{Count: 10, RPS: 12}, profileWith(request.Thresholds{MinRPS: f64(45)}))
	require.Equal(t, 1, e.ExitCode)

	e = Evaluate(metronome.Snapshot{Count: 10, RPS: 60}, profileWith(request.Thresholds{MinRPS: f64(45)}))
	require.Equal(t, 0, e.ExitCode)
}

// TRAP 1. metronome counts Saturated inside Errors and ErrorRate on purpose, so
// saturation is visible rather than silent. A naive error_rate check therefore
// blames the TARGET for the generator running out of workers. Saturated units
// never reached the target, so they belong in neither numerator nor denominator.
func TestSaturationDoesNotInflateTargetErrorRate(t *testing.T) {
	snap := metronome.Snapshot{
		Count: 1000, Errors: 200, Saturated: 200, // every error is saturation
		ErrorRate: 0.20,
	}
	e := Evaluate(snap, profileWith(request.Thresholds{ErrorRate: f64(0.01)}))

	require.InDelta(t, 0.0, e.TargetErrorRate, 1e-9, "the target produced no errors at all")
	require.Equal(t, 0, e.ExitCode, "saturation must not fail a target threshold")
}

func TestTargetErrorRateExcludesSaturationFromBothTerms(t *testing.T) {
	// 1000 attempts, 100 saturated, 45 genuine failures -> 45/900 = 5%.
	snap := metronome.Snapshot{Count: 1000, Errors: 145, Saturated: 100}
	e := Evaluate(snap, profileWith(request.Thresholds{ErrorRate: f64(0.10)}))
	require.InDelta(t, 0.05, e.TargetErrorRate, 1e-9)
	require.Equal(t, 0, e.ExitCode)

	e = Evaluate(snap, profileWith(request.Thresholds{ErrorRate: f64(0.01)}))
	require.Equal(t, 1, e.ExitCode)
}

func TestTargetErrorRateHandlesTotalSaturation(t *testing.T) {
	// Every single unit saturated: the denominator is zero, not a panic.
	snap := metronome.Snapshot{Count: 500, Errors: 500, Saturated: 500}
	e := Evaluate(snap, profileWith(request.Thresholds{ErrorRate: f64(0.01)}))
	require.InDelta(t, 0.0, e.TargetErrorRate, 1e-9)
}

// TRUST 1. Lag WITHOUT saturation means metronome could not keep the schedule:
// the latency numbers describe quiver, not the target. Exit 3, not 1.
func TestGeneratorLagWithoutSaturationIsATrustFailure(t *testing.T) {
	snap := metronome.Snapshot{Count: 1000, MaxScheduleLag: 800 * time.Millisecond, Saturated: 0}
	e := Evaluate(snap, profileWith(request.Thresholds{}))
	require.Equal(t, 3, e.ExitCode)
	require.Len(t, e.Trust, 1)
	require.Contains(t, e.Trust[0].Detail, "800ms")
}

// In OpenLoop, saturation does not excuse lag. metronome's open-loop dispatcher
// emits a unit that finds no free worker immediately as ErrSaturated rather
// than delaying it, so a busy target cannot be the cause of schedule lag in
// this mode — the pacer is. Suppressing the verdict whenever Saturated was
// non-zero let one saturated unit in ten thousand hide a lag two orders of
// magnitude over budget.
func TestOpenLoopLagIsATrustFailureEvenWithSaturation(t *testing.T) {
	snap := metronome.Snapshot{Count: 10504, MaxScheduleLag: 2894 * time.Millisecond, Saturated: 28}
	e := Evaluate(snap, profileWith(request.Thresholds{}))
	require.Equal(t, 3, e.ExitCode)
	require.Len(t, e.Trust, 1)
	require.Equal(t, "schedule_lag", e.Trust[0].Name)
}

// ClosedLoop is the other half of the same rule. There a worker does not ask
// for its next token until the current unit completes, so a slow target IS the
// lag — absorbed as rate sag, and judged by min_rps and the corrected
// percentiles rather than by a trust verdict. Snapshot.Saturated is documented
// to be always zero in this mode, which is why the old `Saturated == 0` guard
// armed the check here, of all places.
func TestClosedLoopLagIsNotATrustFailure(t *testing.T) {
	p := profileWith(request.Thresholds{})
	p.Pacing = metronome.ClosedLoop
	snap := metronome.Snapshot{Count: 1000, MaxScheduleLag: 30 * time.Second, Saturated: 0}

	e := Evaluate(snap, p)
	require.Equal(t, 0, e.ExitCode)
	require.Empty(t, e.Trust)
}

func TestLagWithinBudgetPasses(t *testing.T) {
	// Rate 50 -> interval 20ms -> budget 100ms.
	snap := metronome.Snapshot{Count: 1000, MaxScheduleLag: 30 * time.Millisecond}
	require.Equal(t, 0, Evaluate(snap, profileWith(request.Thresholds{})).ExitCode)
}

// TRUST 2. A clamped histogram means the percentiles understate reality, so a
// percentile assertion is an assertion on a number that is not real.
func TestClampedHistogramInvalidatesPercentileThresholds(t *testing.T) {
	// Max above the histogram ceiling is what makes the clamping high-side, and
	// therefore what makes the percentiles understate reality. Max is documented
	// to be the true maximum whether or not the histogram clamped, so it is the
	// only field that can tell the two ends apart.
	snap := metronome.Snapshot{
		Count: 1000, P99: 40 * time.Millisecond, Clamped: 7,
		Max: 90 * time.Second,
	}

	// Declared raw percentile threshold -> trust failure.
	e := Evaluate(snap, profileWith(request.Thresholds{P99: secs(250 * time.Millisecond)}))
	require.Equal(t, 3, e.ExitCode)
	require.NotEmpty(t, e.Trust)

	// No percentile threshold declared -> clamping is reported but not fatal.
	e = Evaluate(snap, profileWith(request.Thresholds{ErrorRate: f64(0.5)}))
	require.Equal(t, 0, e.ExitCode)
}

// Clamping at the BOTTOM of the histogram is routine and harmless: the value is
// recorded as the floor, so the percentiles round up and a "must be under X"
// threshold cannot pass when it should have failed. A fast loopback target
// produces it constantly, and on a host whose monotonic clock is coarser than a
// microsecond — Windows, at roughly half a millisecond — nearly every result
// clamps this way. Failing on it made exit 3 the normal outcome of a healthy
// run, and made the shipped load example unrunnable.
func TestLowSideClampingIsNotATrustFailure(t *testing.T) {
	snap := metronome.Snapshot{
		Count: 1000, P50: time.Microsecond, P99: time.Millisecond,
		Clamped: 990, CorrectedClamped: 990,
		Max: 2 * time.Millisecond, // nowhere near the ceiling
	}
	e := Evaluate(snap, profileWith(request.Thresholds{
		P99:          secs(100 * time.Millisecond),
		CorrectedP99: secs(100 * time.Millisecond),
	}))
	require.Equal(t, 0, e.ExitCode)
	require.Empty(t, e.Trust)
}

func TestCorrectedClampingOnlyAffectsCorrectedThresholds(t *testing.T) {
	// A corrected value is a latency plus its queueing delay, so it is Max plus
	// the largest lag that has to clear the ceiling — which it does here while
	// the raw Max alone stays under it, exactly the "CorrectedClamped > 0 with
	// Clamped == 0" case metronome documents. The declared max_schedule_lag is
	// generous enough to cover the lag, so this test is about clamping alone.
	snap := metronome.Snapshot{
		Count: 1000, CorrectedP99: 40 * time.Millisecond, CorrectedClamped: 3,
		Max: 59 * time.Second, MaxScheduleLag: 5 * time.Second,
	}
	lagBudget := secs(10 * time.Second)

	e := Evaluate(snap, profileWith(request.Thresholds{
		CorrectedP99: secs(time.Second), MaxScheduleLag: lagBudget}))
	require.Equal(t, 3, e.ExitCode)

	// A raw threshold is unaffected by corrected clamping.
	e = Evaluate(snap, profileWith(request.Thresholds{
		P99: secs(time.Second), MaxScheduleLag: lagBudget}))
	require.Equal(t, 0, e.ExitCode)
}

// If the numbers cannot be trusted, a verdict derived from them cannot be either.
func TestTrustFailureBeatsThresholdFailure(t *testing.T) {
	snap := metronome.Snapshot{
		Count: 1000, P99: 900 * time.Millisecond,
		MaxScheduleLag: 800 * time.Millisecond, Saturated: 0,
	}
	e := Evaluate(snap, profileWith(request.Thresholds{P99: secs(250 * time.Millisecond)}))
	require.Equal(t, 3, e.ExitCode)
	require.NotEmpty(t, e.Thresholds) // both are reported
	require.NotEmpty(t, e.Trust)
}

// --allow-lag downgrades the trust verdict for known-noisy CI runners.
func TestAllowLagDowngradesTrustFailure(t *testing.T) {
	p := profileWith(request.Thresholds{})
	p.AllowLag = true
	snap := metronome.Snapshot{Count: 1000, MaxScheduleLag: 800 * time.Millisecond}

	e := Evaluate(snap, p)
	require.Equal(t, 0, e.ExitCode)
	require.NotEmpty(t, e.Trust, "still reported, just not fatal")
	require.False(t, e.Trust[0].Passed)
}

// A run that delivered nothing is not a pass.
func TestZeroResultsIsATrustFailure(t *testing.T) {
	e := Evaluate(metronome.Snapshot{}, profileWith(request.Thresholds{}))
	require.Equal(t, 3, e.ExitCode)
	require.Contains(t, e.Trust[0].Detail, "no results")
}

// --allow-lag waives schedule_lag and nothing else. Its help text promises a
// "generator-lag" waiver, and a noisy CI runner is the only thing it exists to
// absorb; letting it also swallow "the histogram clamped" or "nothing was
// recorded" would turn the flag into a blanket --ignore-trust and hand back an
// exit 0 for numbers quiver has just said are not real.
func TestAllowLagDoesNotWaiveNonLagTrustFailures(t *testing.T) {
	p := profileWith(request.Thresholds{P99: secs(time.Second)})
	p.AllowLag = true

	// A clamped ceiling still invalidates the percentiles.
	e := Evaluate(metronome.Snapshot{
		Count: 1000, Clamped: 5, Max: 90 * time.Second,
	}, p)
	require.Equal(t, 3, e.ExitCode)

	// A run that recorded nothing is still not a pass.
	e = Evaluate(metronome.Snapshot{}, p)
	require.Equal(t, 3, e.ExitCode)

	// And the lag verdict it IS meant to waive still gets waived, even when it
	// arrives alongside a passing threshold.
	e = Evaluate(metronome.Snapshot{
		Count: 1000, MaxScheduleLag: 800 * time.Millisecond,
	}, p)
	require.Equal(t, 0, e.ExitCode)
	require.NotEmpty(t, e.Trust, "still reported, just not fatal")
}
