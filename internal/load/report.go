package load

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/RomanAgaltsev/metronome"
	"github.com/fatih/color"

	"github.com/RomanAgaltsev/quiver/internal/secret"
)

// Run is a finished load run: what was driven, what came back, and the verdict.
type Run struct {
	Target   string
	Profile  *Profile
	Snapshot metronome.Snapshot
	Eval     Evaluation

	// ByRequest is one entry per request in a folder target, sorted by name.
	// Empty for a run that was never broken down.
	ByRequest []requestStats

	// Warmup is the excluded prefix, zero when --warmup was not set. Skipped is
	// how many Results it kept out of Snapshot. Snapshot.Count + Skipped is the
	// whole population the run produced, which is what makes the exclusion
	// auditable rather than a number that quietly shrank.
	Warmup  time.Duration
	Skipped int64
}

// requestStats is one endpoint's slice of a load run.
type requestStats struct {
	Name string
	Snap metronome.Snapshot
}

// breakdown reads the per-request series back off the LabeledStats, sorted by
// name so text and JSON output are stable between runs.
//
// The label it keys on is the one runner.go has stamped since v1.1.0. Until the
// pin reached metronome v0.6 there was nothing that could read it back, so a
// folder target reported one p99 describing no endpoint in it.
//
// Series returns the child recorders rather than their snapshots, so each is
// snapshotted here.
func breakdown(ls *metronome.LabeledStats[*metronome.Stats]) []requestStats {
	// nil for a single-target run, which is not broken down at all.
	if ls == nil {
		return nil
	}
	series := ls.Series()
	out := make([]requestStats, 0, len(series))
	for name, child := range series {
		out = append(out, requestStats{Name: name, Snap: child.Snapshot()})
	}
	slices.SortFunc(out, func(a, b requestStats) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
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
	fmt.Fprintf(&b, "%s  ·  %s\n\n", fmtBound(r.Profile), r.Profile.Describe())

	snap := r.Snapshot
	fmt.Fprintf(&b, "requests    %-9d errors %d (%.2f%%)     saturated %d\n",
		snap.Count, snap.Errors-snap.Saturated, r.Eval.TargetErrorRate*100, snap.Saturated)
	// When units saturated, the rate the generator recorded and the rate the
	// target served are different numbers, and only the second one says anything
	// about the target. Showing one without the other is how a run in which
	// nothing was sent could read as healthy.
	if snap.Saturated > 0 {
		fmt.Fprintf(&b, "achieved    %-9s throughput %s   (%.1f/s recorded incl. saturated)\n\n",
			fmt.Sprintf("%.1f/s", r.Eval.AttemptedRPS), fmtBytesPerSec(snap.Throughput), snap.RPS)
	} else {
		fmt.Fprintf(&b, "achieved    %-9s throughput %s\n\n",
			fmt.Sprintf("%.1f/s", snap.RPS), fmtBytesPerSec(snap.Throughput))
	}

	// Raw and corrected are ALWAYS shown together: metronome's docs are explicit
	// that they are read as a pair, and a large gap is the signal that the raw
	// numbers understate what a schedule-faithful client would have suffered.
	fmt.Fprintf(&b, "latency          %8s %8s %8s %8s\n", "p50", "p95", "p99", "max")
	fmt.Fprintf(&b, "  raw           %8s %8s %8s %8s\n",
		ms(snap.P50), ms(snap.P95), ms(snap.P99), ms(snap.Max))
	fmt.Fprintf(&b, "  corrected     %8s %8s %8s %8s\n\n",
		ms(snap.CorrectedP50), ms(snap.CorrectedP95), ms(snap.CorrectedP99), "—")

	// Read off the lag itself, not off "any trust verdict failed": a clamped
	// histogram is not a statement about the schedule, and labelling the lag
	// line SUSPECT because of one pointed at the wrong number.
	lagState := "OK"
	if snap.MaxScheduleLag > r.Profile.LagBudget() {
		lagState = "SUSPECT"
	}
	fmt.Fprintf(&b, "schedule lag    max %s  (budget %s)   %s\n",
		ms(snap.MaxScheduleLag), r.Profile.LagBudget(), lagState)

	// Count + Skipped is the whole population the run produced. "measured 800 of
	// 1000" is the honest line; "800 requests" on its own invites the reader to
	// wonder where the rest went, and a threshold judged over a silently reduced
	// population is exactly what this exists to prevent.
	if r.Warmup > 0 {
		fmt.Fprintf(&b, "measured        %d of %d requests  (%s warmup excluded)\n",
			snap.Count, snap.Count+r.Skipped, fmtDuration(r.Warmup))
	}

	writeBreakdown(&b, r.ByRequest)

	if len(r.Eval.Thresholds) > 0 || len(r.Eval.Trust) > 0 {
		b.WriteString("\n")
	}
	for _, v := range r.Eval.Thresholds {
		fmt.Fprintf(&b, "[%s] %-16s %s\n", mark(v.Passed, opts.Color), v.Name, v.Detail)
	}
	for _, v := range r.Eval.Trust {
		fmt.Fprintf(&b, "[%s] %-16s %s\n", mark(v.Passed, opts.Color), v.Name, v.Detail)
	}
	if r.Eval.ExitCode == 3 {
		b.WriteString("\nThe measurement is not trustworthy: these numbers describe the\n" +
			"generator, not the target.\n")
		// --allow-lag waives schedule_lag and nothing else, so offering it for a
		// clamped histogram would send the reader after a flag that cannot help.
		if failedNamed(r.Eval.Trust, verdictScheduleLag) {
			b.WriteString("Pass --allow-lag to downgrade the schedule-lag verdict to a warning.\n")
		}
	}

	_, err := io.WriteString(w, red.String(b.String()))
	return err
}

// writeBreakdown prints the per-endpoint section.
//
// It is omitted for fewer than two series: a single-target run's only row
// repeats the total line above it, and a section that adds nothing trains the
// reader to skip the one that does.
//
// A row carries its own clamp marker rather than deferring to the total's. One
// slow endpoint clamping its own histogram while the total's is fine is a more
// likely shape than the total clamping, and a clamped percentile understates
// reality — reporting the number without that is the failure this exists to
// prevent.
func writeBreakdown(b *strings.Builder, rows []requestStats) {
	if len(rows) < 2 {
		return
	}
	fmt.Fprintf(b, "\nper request     %8s %8s %8s %8s\n", "reqs", "err", "p50", "p99")
	for _, row := range rows {
		mark := ""
		if row.Snap.Clamped > 0 || row.Snap.CorrectedClamped > 0 {
			mark = "  ! clamped, percentiles understate"
		}
		fmt.Fprintf(b, "  %-12s %8d %8d %8s %8s%s\n",
			truncate(row.Name, 12), row.Snap.Count, row.Snap.Errors,
			ms(row.Snap.P50), ms(row.Snap.P99), mark)
	}
}

// truncate keeps the breakdown's columns aligned when a request name is longer
// than its cell.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func breakdownJSON(rows []requestStats) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"name": row.Name, "count": row.Snap.Count, "errors": row.Snap.Errors,
			"saturated": row.Snap.Saturated,
			"p50":       row.Snap.P50.String(), "p95": row.Snap.P95.String(),
			"p99": row.Snap.P99.String(), "max": row.Snap.Max.String(),
			"corrected_p99": row.Snap.CorrectedP99.String(),
			// Per-series clamp state is reported even when the total's is zero.
			"clamped": row.Snap.Clamped, "corrected_clamped": row.Snap.CorrectedClamped,
		})
	}
	return out
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
		// attempted is Count less the units that never found a free worker, and
		// attempted_rps is the rate the target served. rps above is what the
		// generator recorded, saturated units included.
		"attempted":     r.Eval.Attempted,
		"attempted_rps": r.Eval.AttemptedRPS,
		"thresholds":    verdictsJSON(r.Eval.Thresholds),
		"trust":         verdictsJSON(r.Eval.Trust),
		// Always present, empty for a single-target run, so a consumer can index
		// it without a nil check.
		"by_request": breakdownJSON(r.ByRequest),
		// count + skipped is the whole population; warmup is what excluded the
		// difference. Both are always present so a CI consumer can assert on the
		// measured population without branching on whether warmup was set.
		"warmup":  r.Warmup.String(),
		"skipped": r.Skipped,
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

// progressWriter prints a trailing-window view of the run.
//
// Every field it prints comes from RollingStats.Window(), a snapshot over the
// last window worth of Results rather than the whole run. That is what makes
// live percentiles and live lag honest, and it is why quiver refused to print
// them at all while it held a cumulative Snapshot: one early stall would have
// pinned lag red for the rest of the run.
//
// The window covers traffic sent during --warmup as well. Warmup is excluded
// from the report, not from what is happening now -- a progress line printing
// zeros while the pool warms looks like a hung run.
type progressWriter struct {
	w       io.Writer
	every   time.Duration
	elapsed time.Duration
}

func newProgressWriter(w io.Writer, every time.Duration) *progressWriter {
	if every <= 0 {
		every = time.Second
	}
	return &progressWriter{w: w, every: every}
}

// tick prints one line from a trailing-window snapshot. No deltas are derived:
// the window is already the current view, and subtracting the previous tick on
// top of it would halve the reported rate.
//
// The rate column is Snapshot.RPS. Snapshot.Throughput is bytes per second and
// belongs in the report's throughput line, not beside a request count.
func (p *progressWriter) tick(win metronome.Snapshot) {
	p.elapsed += p.every
	_, _ = fmt.Fprintf(p.w,
		"%6s  %6d reqs  %4d err  %7.1f/s   p50 %-8s p99 %-8s lag %s\n",
		fmtDuration(p.elapsed), win.Count, win.Errors, win.RPS,
		ms(win.P50), ms(win.P99), ms(win.MaxScheduleLag))
}

// failedNamed reports whether a named verdict is present and failing.
func failedNamed(vs []Verdict, name string) bool {
	for _, v := range vs {
		if v.Name == name && !v.Passed {
			return true
		}
	}
	return false
}

// forceColor emits escapes regardless of fatih/color's own TTY auto-detection:
// ReportOptions.Color is already the decision, made by render.ShouldColor.
func forceColor(attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	c.EnableColor()
	return c
}

var (
	okColor   = forceColor(color.FgGreen)
	failColor = forceColor(color.FgRed)
)

func mark(passed, colorize bool) string {
	label := "FAIL"
	c := failColor
	if passed {
		label, c = "PASS", okColor
	}
	if !colorize {
		return label
	}
	return c.Sprint(label)
}

func ms(d time.Duration) string {
	if d == 0 {
		return "—"
	}
	return d.Round(time.Millisecond).String()
}

func fmtDuration(d time.Duration) string { return d.Round(time.Second).String() }

// fmtBound describes what actually stopped the run. A requests-bounded profile
// has no duration, and printing fmtDuration's "0s" claimed a run length the
// profile never declared.
func fmtBound(p *Profile) string {
	switch {
	case p.Duration > 0 && p.Requests > 0:
		return fmt.Sprintf("%s or %d requests", fmtDuration(p.Duration), p.Requests)
	case p.Requests > 0:
		return fmt.Sprintf("%d requests", p.Requests)
	default:
		return fmtDuration(p.Duration)
	}
}

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
