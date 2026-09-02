package load

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/RomanAgaltsev/metronome"

	"github.com/RomanAgaltsev/quiver/internal/secret"
)

// Run is a finished load run: what was driven, what came back, and the verdict.
type Run struct {
	Target   string
	Profile  *Profile
	Snapshot metronome.Snapshot
	Eval     Evaluation
}

// ReportOptions controls report rendering. Redactor may be nil (redacts nothing).
type ReportOptions struct {
	Format   string // "pretty" | "json"
	Color    bool
	Redactor *secret.Redactor
}

// WriteReport renders a finished run.
func WriteReport(w io.Writer, r Run, opts ReportOptions) error {
	switch opts.Format {
	case "pretty":
		return writePretty(w, r, opts)
	case "json":
		return writeJSON(w, r, opts)
	default:
		return fmt.Errorf("load: unknown output format %q (want pretty or json)", opts.Format)
	}
}

func writePretty(w io.Writer, r Run, opts ReportOptions) error {
	red := opts.Redactor
	var b strings.Builder

	fmt.Fprintf(&b, "target      %s\n", r.Target)
	fmt.Fprintf(&b, "%s  ·  %s\n\n", fmtDuration(r.Profile.Duration), r.Profile.Describe())

	snap := r.Snapshot
	fmt.Fprintf(&b, "requests    %-9d errors %d (%.2f%%)     saturated %d\n",
		snap.Count, snap.Errors-snap.Saturated, r.Eval.TargetErrorRate*100, snap.Saturated)
	fmt.Fprintf(&b, "achieved    %-9s throughput %s\n\n",
		fmt.Sprintf("%.1f/s", snap.RPS), fmtBytesPerSec(snap.Throughput))

	// Raw and corrected are ALWAYS shown together: metronome's docs are explicit
	// that they are read as a pair, and a large gap is the signal that the raw
	// numbers understate what a schedule-faithful client would have suffered.
	fmt.Fprintf(&b, "latency          %8s %8s %8s %8s\n", "p50", "p95", "p99", "max")
	fmt.Fprintf(&b, "  raw           %8s %8s %8s %8s\n",
		ms(snap.P50), ms(snap.P95), ms(snap.P99), ms(snap.Max))
	fmt.Fprintf(&b, "  corrected     %8s %8s %8s %8s\n\n",
		ms(snap.CorrectedP50), ms(snap.CorrectedP95), ms(snap.CorrectedP99), "—")

	lagState := "OK"
	if anyFailed(r.Eval.Trust) {
		lagState = "SUSPECT"
	}
	fmt.Fprintf(&b, "schedule lag    max %s  (budget %s)   %s\n",
		ms(snap.MaxScheduleLag), r.Profile.LagBudget(), lagState)

	if len(r.Eval.Thresholds) > 0 || len(r.Eval.Trust) > 0 {
		b.WriteString("\n")
	}
	for _, v := range r.Eval.Thresholds {
		fmt.Fprintf(&b, "[%s] %-16s %s\n", mark(v.Passed), v.Name, v.Detail)
	}
	for _, v := range r.Eval.Trust {
		fmt.Fprintf(&b, "[%s] %-16s %s\n", mark(v.Passed), v.Name, v.Detail)
	}
	if r.Eval.ExitCode == 3 {
		b.WriteString("\nThe measurement is not trustworthy: these numbers describe the\n" +
			"generator, not the target. Pass --allow-lag to downgrade this to a warning.\n")
	}

	_, err := io.WriteString(w, red.String(b.String()))
	return err
}

func writeJSON(w io.Writer, r Run, opts ReportOptions) error {
	snap := r.Snapshot
	codes := make(map[string]int64, len(snap.Codes))
	for k, v := range snap.Codes {
		codes[k] = v
	}
	out := map[string]any{
		"target":    r.Target,
		"exit_code": r.Eval.ExitCode,
		"profile": map[string]any{
			"describe":    r.Profile.Describe(),
			"duration":    r.Profile.Duration.String(),
			"lag_budget":  r.Profile.LagBudget().String(),
			"concurrency": r.Profile.Concurrency,
		},
		"snapshot": map[string]any{
			"count": snap.Count, "errors": snap.Errors, "saturated": snap.Saturated,
			"rps": snap.RPS, "error_rate": snap.ErrorRate,
			"p50": snap.P50.String(), "p95": snap.P95.String(), "p99": snap.P99.String(),
			"max":              snap.Max.String(),
			"corrected_p50":    snap.CorrectedP50.String(),
			"corrected_p95":    snap.CorrectedP95.String(),
			"corrected_p99":    snap.CorrectedP99.String(),
			"corrected_count":  snap.CorrectedCount,
			"max_schedule_lag": snap.MaxScheduleLag.String(),
			"clamped":          snap.Clamped, "corrected_clamped": snap.CorrectedClamped,
			"bytes": snap.Bytes, "throughput": snap.Throughput,
			"codes": codes,
		},
		// Reported separately from snapshot.error_rate, which is metronome's raw
		// figure including saturation. This is the number thresholds judge.
		"target_error_rate": r.Eval.TargetErrorRate,
		"thresholds":        verdictsJSON(r.Eval.Thresholds),
		"trust":             verdictsJSON(r.Eval.Trust),
	}

	buf, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if _, err := w.Write(opts.Redactor.Bytes(buf)); err != nil {
		return err
	}
	_, err = io.WriteString(w, "\n")
	return err
}

func verdictsJSON(vs []Verdict) []map[string]any {
	out := make([]map[string]any, 0, len(vs))
	for _, v := range vs {
		out = append(out, map[string]any{"name": v.Name, "passed": v.Passed, "detail": v.Detail})
	}
	return out
}

// progressWriter prints inter-tick deltas during a run.
//
// It prints ONLY count, errors and achieved rate. Every Snapshot field is
// cumulative, so live percentiles or a live MaxScheduleLag would be lifetime
// figures presented as current ones — one early stall would pin lag red for the
// rest of the run. Those arrive when metronome ships rolling-window Stats
// (its v0.5); until then quiver declines to print a number it cannot stand behind.
type progressWriter struct {
	w         io.Writer
	every     time.Duration
	started   time.Time
	lastCount int64
	lastErrs  int64
	elapsed   time.Duration
}

func newProgressWriter(w io.Writer, every time.Duration) *progressWriter {
	if every <= 0 {
		every = time.Second
	}
	return &progressWriter{w: w, every: every, started: time.Now()}
}

func (p *progressWriter) tick(snap metronome.Snapshot) {
	p.elapsed += p.every
	dCount := snap.Count - p.lastCount
	dErrs := snap.Errors - p.lastErrs
	p.lastCount, p.lastErrs = snap.Count, snap.Errors

	rate := float64(dCount) / p.every.Seconds()
	fmt.Fprintf(p.w, "%6s  %6d reqs  %4d err  %7.1f/s\n",
		fmtDuration(p.elapsed), snap.Count, dErrs, rate)
}

func mark(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

func ms(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	return d.Round(time.Millisecond).String()
}

func fmtDuration(d time.Duration) string { return d.Round(time.Second).String() }

func fmtBytesPerSec(bps float64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", bps/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.1f KB/s", bps/(1<<10))
	default:
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

var _ = sort.Strings // retained if a future field needs deterministic ordering
