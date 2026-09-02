package load

import (
	"testing"
	"time"

	"github.com/RomanAgaltsev/metronome"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

func secs(d time.Duration) request.Duration {
	return request.NewDuration(d)
}

// CLI flags override the load: block, exactly as --var overrides an environment.
func TestResolveProfilePrecedence(t *testing.T) {
	spec := &request.LoadSpec{Rate: 10, Duration: secs(30 * time.Second), Concurrency: 5, Pacing: "closed"}

	p, err := ResolveProfile(spec, Overrides{})
	require.NoError(t, err)
	require.Equal(t, 10.0, p.Rate)
	require.Equal(t, 30*time.Second, p.Duration)
	require.Equal(t, 5, p.Concurrency)
	require.Equal(t, metronome.ClosedLoop, p.Pacing)

	p, err = ResolveProfile(spec, Overrides{Rate: 99, Duration: time.Minute, Concurrency: 50, Pacing: "open"})
	require.NoError(t, err)
	require.Equal(t, 99.0, p.Rate)
	require.Equal(t, time.Minute, p.Duration)
	require.Equal(t, 50, p.Concurrency)
	require.Equal(t, metronome.OpenLoop, p.Pacing)
}

// OpenLoop is the default when nothing says otherwise.
func TestResolveProfileDefaultsToOpenLoop(t *testing.T) {
	p, err := ResolveProfile(&request.LoadSpec{Rate: 1, Duration: secs(time.Second)}, Overrides{})
	require.NoError(t, err)
	require.Equal(t, metronome.OpenLoop, p.Pacing)
	require.True(t, p.Assertions)
}

// A run driven entirely from flags, with no load: block at all.
func TestResolveProfileFromFlagsOnly(t *testing.T) {
	p, err := ResolveProfile(nil, Overrides{Rate: 25, Duration: 10 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 25.0, p.Rate)
	require.Equal(t, 10*time.Second, p.Duration)
}

func TestResolveProfileValidatesAfterOverlay(t *testing.T) {
	// A ramp with no duration in the file becomes valid once --duration supplies one.
	spec := &request.LoadSpec{Ramp: &request.RampSpec{Start: 1, End: 10}, Requests: 100}
	_, err := ResolveProfile(spec, Overrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duration")

	p, err := ResolveProfile(spec, Overrides{Duration: 20 * time.Second})
	require.NoError(t, err)
	require.NotNil(t, p.Ramp)
}

func TestResolveProfileRejectsEmptyProfile(t *testing.T) {
	_, err := ResolveProfile(nil, Overrides{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rate")
}

func TestControllerShapes(t *testing.T) {
	constant, err := ResolveProfile(&request.LoadSpec{Rate: 50, Duration: secs(time.Second)}, Overrides{})
	require.NoError(t, err)
	require.Equal(t, 50.0, constant.Controller().Rate(0))

	ramped, err := ResolveProfile(&request.LoadSpec{
		Ramp: &request.RampSpec{Start: 10, End: 110}, Duration: secs(10 * time.Second)}, Overrides{})
	require.NoError(t, err)
	rc := ramped.Controller()
	require.Equal(t, 10.0, rc.Rate(0))
	require.Equal(t, 110.0, rc.Rate(10*time.Second))
	require.InDelta(t, 60.0, rc.Rate(5*time.Second), 0.001)

	phased, err := ResolveProfile(&request.LoadSpec{
		Phases: []request.PhaseSpec{
			{Duration: secs(10 * time.Second), Rate: 10},
			{Duration: secs(10 * time.Second), Rate: 90},
		}, Duration: secs(20 * time.Second)}, Overrides{})
	require.NoError(t, err)
	pc := phased.Controller()
	require.Equal(t, 10.0, pc.Rate(time.Second))
	require.Equal(t, 90.0, pc.Rate(15*time.Second))
}

// --ramp on the command line overrides a constant rate in the file.
func TestRampOverrideReplacesRate(t *testing.T) {
	p, err := ResolveProfile(
		&request.LoadSpec{Rate: 10, Duration: secs(30 * time.Second)},
		Overrides{RampSet: true, RampStart: 5, RampEnd: 50},
	)
	require.NoError(t, err)
	require.NotNil(t, p.Ramp)
	require.Equal(t, 0.0, p.Rate)
	require.Equal(t, 5.0, p.Controller().Rate(0))
}

// max(25ms, 5 x target interval): a handful of intervals late is noise;
// hundreds is a broken measurement. Scaling avoids a constant that is far too
// tight at 5000 rps and far too loose at 50.
func TestLagBudget(t *testing.T) {
	slow, _ := ResolveProfile(&request.LoadSpec{Rate: 50, Duration: secs(time.Second)}, Overrides{})
	require.Equal(t, 100*time.Millisecond, slow.LagBudget()) // 5 x 20ms

	fast, _ := ResolveProfile(&request.LoadSpec{Rate: 5000, Duration: secs(time.Second)}, Overrides{})
	require.Equal(t, 25*time.Millisecond, fast.LagBudget()) // floor

	explicit, _ := ResolveProfile(&request.LoadSpec{
		Rate: 50, Duration: secs(time.Second),
		Thresholds: &request.Thresholds{MaxScheduleLag: secs(7 * time.Millisecond)}}, Overrides{})
	require.Equal(t, 7*time.Millisecond, explicit.LagBudget())
}

// A ramp's budget uses its highest rate — the tightest interval it will reach.
func TestLagBudgetUsesPeakRateForRamp(t *testing.T) {
	p, _ := ResolveProfile(&request.LoadSpec{
		Ramp: &request.RampSpec{Start: 10, End: 1000}, Duration: secs(10 * time.Second)}, Overrides{})
	require.Equal(t, 25*time.Millisecond, p.LagBudget()) // 5 x 1ms floored
}
