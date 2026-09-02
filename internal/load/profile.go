package load

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/RomanAgaltsev/metronome"

	"github.com/RomanAgaltsev/quiver/internal/request"
)

// minLagBudget floors the schedule-lag budget so a high-rate run does not fail
// on sub-millisecond scheduling noise.
const minLagBudget = 25 * time.Millisecond

// lagBudgetIntervals is how many nominal send intervals the generator may fall
// behind before its numbers stop being trustworthy.
const lagBudgetIntervals = 5

// Overrides carries the CLI flags that take precedence over a load: block.
// A zero value means "not set" for every field.
type Overrides struct {
	Rate        float64
	RampStart   float64
	RampEnd     float64
	RampSet     bool
	Duration    time.Duration
	Requests    int
	Concurrency int
	Pacing      string
	AllowLag    bool
}

// Profile is a fully resolved load configuration: the load: block with CLI
// overrides applied and validated.
type Profile struct {
	Rate        float64
	Ramp        *request.RampSpec
	Phases      []request.PhaseSpec
	Duration    time.Duration
	Requests    int
	Concurrency int
	Pacing      metronome.PacingMode
	Assertions  bool
	Thresholds  request.Thresholds
	AllowLag    bool
}

// ResolveProfile overlays CLI overrides onto a load: block and validates the
// result. Precedence matches the MVP's variable rule: the file declares the
// durable contract, a flag is the one-off override.
func ResolveProfile(spec *request.LoadSpec, ov Overrides) (*Profile, error) {
	merged := request.LoadSpec{}
	if spec != nil {
		merged = *spec
	}

	// A --ramp override replaces whatever rate shape the file declared, so the
	// "exactly one shape" rule still holds after the overlay.
	if ov.RampSet {
		merged.Ramp = &request.RampSpec{Start: ov.RampStart, End: ov.RampEnd}
		merged.Rate = 0
		merged.Phases = nil
	} else if ov.Rate > 0 {
		merged.Rate = ov.Rate
		merged.Ramp = nil
		merged.Phases = nil
	}

	if ov.Duration > 0 {
		merged.Duration = request.NewDuration(ov.Duration)
	}
	if ov.Requests > 0 {
		merged.Requests = ov.Requests
	}
	if ov.Concurrency > 0 {
		merged.Concurrency = ov.Concurrency
	}
	if ov.Pacing != "" {
		merged.Pacing = ov.Pacing
	}

	if err := merged.Validate("load"); err != nil {
		return nil, err
	}

	p := &Profile{
		Rate:        merged.Rate,
		Ramp:        merged.Ramp,
		Phases:      merged.Phases,
		Duration:    merged.Duration.Duration(),
		Requests:    merged.Requests,
		Concurrency: merged.Concurrency,
		Pacing:      metronome.OpenLoop, // spec §3: quiver's default
		Assertions:  merged.AssertionsEnabled(),
		AllowLag:    ov.AllowLag,
	}
	if merged.Pacing == "closed" {
		p.Pacing = metronome.ClosedLoop
	}
	if merged.Thresholds != nil {
		p.Thresholds = *merged.Thresholds
	}
	return p, nil
}

// Controller returns the metronome RateController for this profile's shape.
func (p *Profile) Controller() metronome.RateController {
	switch {
	case p.Ramp != nil:
		return metronome.Ramp{Start: p.Ramp.Start, End: p.Ramp.End, Over: p.Duration}
	case len(p.Phases) > 0:
		phases := make([]metronome.Phase, 0, len(p.Phases))
		for _, ph := range p.Phases {
			// NOTE: metronome's field is TargetRPS, not Rate.
			phases = append(phases, metronome.Phase{Duration: ph.Duration.Duration(), TargetRPS: ph.Rate})
		}
		return metronome.Phased{Phases: phases}
	default:
		return metronome.Constant(p.Rate)
	}
}

// peakRate is the highest rate this profile will ask for, i.e. its tightest
// send interval.
func (p *Profile) peakRate() float64 {
	switch {
	case p.Ramp != nil:
		return math.Max(p.Ramp.Start, p.Ramp.End)
	case len(p.Phases) > 0:
		peak := 0.0
		for _, ph := range p.Phases {
			peak = math.Max(peak, ph.Rate)
		}
		return peak
	default:
		return p.Rate
	}
}

// LagBudget is how far the generator may fall behind its own schedule before
// the run's numbers stop describing the target.
//
// The default scales with rate — max(25ms, 5 intervals) — because a fixed
// constant is far too tight at 5,000 rps and far too loose at 50.
func (p *Profile) LagBudget() time.Duration {
	if declared := p.Thresholds.MaxScheduleLag.Duration(); declared > 0 {
		return declared
	}
	peak := p.peakRate()
	if peak <= 0 {
		return minLagBudget
	}
	interval := time.Duration(float64(time.Second) / peak)
	if budget := lagBudgetIntervals * interval; budget > minLagBudget {
		return budget
	}
	return minLagBudget
}

// Describe renders the profile for the report header, e.g.
// "rate 50/s constant · concurrency 50 · pacing open".
func (p *Profile) Describe() string {
	var shape string
	switch {
	case p.Ramp != nil:
		shape = fmt.Sprintf("ramp %g/s → %g/s", p.Ramp.Start, p.Ramp.End)
	case len(p.Phases) > 0:
		shape = fmt.Sprintf("%d phases", len(p.Phases))
	default:
		shape = fmt.Sprintf("rate %g/s constant", p.Rate)
	}
	pacing := "open"
	if p.Pacing == metronome.ClosedLoop {
		pacing = "closed"
	}
	parts := []string{shape}
	if p.Concurrency > 0 {
		parts = append(parts, fmt.Sprintf("concurrency %d", p.Concurrency))
	}
	parts = append(parts, "pacing "+pacing)
	return strings.Join(parts, "  ·  ")
}
