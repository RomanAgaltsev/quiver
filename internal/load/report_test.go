package load

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RomanAgaltsev/metronome"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/secret"
)

var update = flag.Bool("update", false, "update golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run `go test ./internal/load/... -update` to create it")
	require.Equal(t, string(want), string(got))
}

func sampleRun() Run {
	p := &Profile{Rate: 50, Duration: 30 * time.Second, Concurrency: 50, Pacing: metronome.OpenLoop,
		Thresholds: request.Thresholds{P99: secs(250 * time.Millisecond), ErrorRate: f64(0.01)}}
	snap := metronome.Snapshot{
		Count: 1500, Errors: 3, RPS: 49.8, ErrorRate: 0.002,
		P50: 12 * time.Millisecond, P95: 34 * time.Millisecond, P99: 48 * time.Millisecond,
		Max:          91 * time.Millisecond,
		CorrectedP50: 12 * time.Millisecond, CorrectedP95: 35 * time.Millisecond,
		CorrectedP99:   52 * time.Millisecond,
		MaxScheduleLag: 3 * time.Millisecond, Bytes: 36_000_000, Throughput: 1_200_000,
		Codes: map[string]int64{"200 OK": 1497, "503 Service Unavailable": 3},
	}
	return Run{Target: "GET https://api.example.com/users", Profile: p, Snapshot: snap,
		Eval: Evaluate(snap, p)}
}

func TestWriteReportPrettyGolden(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteReport(&buf, sampleRun(), ReportOptions{Format: "pretty", Redactor: secret.NewRedactor(nil)}))
	golden(t, "report_pretty", buf.Bytes())
}

// Raw and corrected percentiles are shown as a PAIR, always: metronome's docs
// are explicit that they must be read together, because a large gap means the
// generator queued and the raw numbers understate what a client would suffer.
func TestPrettyReportAlwaysPairsRawAndCorrected(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteReport(&buf, sampleRun(), ReportOptions{Format: "pretty", Redactor: secret.NewRedactor(nil)}))
	out := buf.String()
	require.Contains(t, out, "raw")
	require.Contains(t, out, "corrected")
	require.Contains(t, out, "schedule lag")
}

func TestWriteReportJSONIsMachineReadable(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteReport(&buf, sampleRun(), ReportOptions{Format: "json", Redactor: secret.NewRedactor(nil)}))

	var got struct {
		Target   string `json:"target"`
		ExitCode int    `json:"exit_code"`
		Snapshot struct {
			Count          int64   `json:"count"`
			RPS            float64 `json:"rps"`
			MaxScheduleLag string  `json:"max_schedule_lag"`
		} `json:"snapshot"`
		TargetErrorRate float64 `json:"target_error_rate"`
		Thresholds      []struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
		} `json:"thresholds"`
		Trust []any `json:"trust"`
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Equal(t, int64(1500), got.Snapshot.Count)
	require.Equal(t, 0, got.ExitCode)
	require.Len(t, got.Thresholds, 2)
	require.InDelta(t, 0.002, got.TargetErrorRate, 1e-9)
}

func TestReportRedactsSecrets(t *testing.T) {
	r := sampleRun()
	r.Target = "GET https://api.example.com/users?token=s3cret"
	for _, format := range []string{"pretty", "json"} {
		var buf bytes.Buffer
		require.NoError(t, WriteReport(&buf, r, ReportOptions{
			Format: format, Redactor: secret.NewRedactor([]string{"s3cret"})}))
		require.NotContains(t, buf.String(), "s3cret", "format %q leaked", format)
		require.Contains(t, buf.String(), "***")
	}
}

func TestReportShowsFailedVerdicts(t *testing.T) {
	p := &Profile{Rate: 50, Duration: time.Second, Pacing: metronome.OpenLoop,
		Thresholds: request.Thresholds{P99: secs(10 * time.Millisecond)}}
	snap := metronome.Snapshot{Count: 100, P99: 900 * time.Millisecond}
	r := Run{Target: "GET /x", Profile: p, Snapshot: snap, Eval: Evaluate(snap, p)}

	var buf bytes.Buffer
	require.NoError(t, WriteReport(&buf, r, ReportOptions{Format: "pretty", Redactor: secret.NewRedactor(nil)}))
	require.Contains(t, buf.String(), "FAIL")
	require.Contains(t, buf.String(), "p99")
}

func TestReportUnknownFormat(t *testing.T) {
	require.Error(t, WriteReport(&bytes.Buffer{}, sampleRun(), ReportOptions{Format: "xml"}))
}

// Progress prints inter-tick DELTAS quiver computes itself. It deliberately
// prints no percentiles and no lag: every Snapshot field is cumulative, so
// those would be lifetime figures presented as current ones. Live versions
// arrive with metronome v0.5's rolling-window Stats.
func TestProgressPrintsDeltasNotLifetimeFigures(t *testing.T) {
	var buf bytes.Buffer
	pw := newProgressWriter(&buf, time.Second)

	pw.tick(metronome.Snapshot{Count: 250, Errors: 0, P99: time.Hour, MaxScheduleLag: time.Hour})
	pw.tick(metronome.Snapshot{Count: 500, Errors: 1, P99: time.Hour, MaxScheduleLag: time.Hour})

	out := buf.String()
	require.Contains(t, out, "250")
	require.Contains(t, out, "500")
	require.NotContains(t, out, "p99")
	require.NotContains(t, out, "lag")
}

// The header states what actually bounded the run. A requests-bounded profile
// has no duration, and printing a rounded zero claimed a run length the profile
// never declared.
func TestPrettyHeaderStatesTheRealStopCondition(t *testing.T) {
	render := func(p *Profile) string {
		var buf bytes.Buffer
		require.NoError(t, WriteReport(&buf, Run{Target: "GET /x", Profile: p,
			Snapshot: metronome.Snapshot{Count: 1}}, ReportOptions{
			Format: "pretty", Redactor: secret.NewRedactor(nil)}))
		return buf.String()
	}

	out := render(&Profile{Rate: 500, Requests: 100, Pacing: metronome.OpenLoop})
	require.Contains(t, out, "100 requests")
	require.NotContains(t, out, "0s  ·")

	out = render(&Profile{Rate: 50, Duration: 30 * time.Second, Pacing: metronome.OpenLoop})
	require.Contains(t, out, "30s  ·")

	out = render(&Profile{Rate: 50, Duration: 30 * time.Second, Requests: 100, Pacing: metronome.OpenLoop})
	require.Contains(t, out, "30s or 100 requests")
}

// The exit-3 footer must not send the reader after a flag that cannot help:
// --allow-lag waives schedule_lag only, so it is offered only for schedule_lag.
func TestExitThreeFooterOffersAllowLagOnlyForLag(t *testing.T) {
	render := func(snap metronome.Snapshot, th request.Thresholds) string {
		p := &Profile{Rate: 50, Duration: time.Second, Pacing: metronome.OpenLoop, Thresholds: th}
		var buf bytes.Buffer
		require.NoError(t, WriteReport(&buf, Run{Target: "GET /x", Profile: p, Snapshot: snap,
			Eval: Evaluate(snap, p)}, ReportOptions{
			Format: "pretty", Redactor: secret.NewRedactor(nil)}))
		return buf.String()
	}

	lag := render(metronome.Snapshot{Count: 100, MaxScheduleLag: 800 * time.Millisecond}, request.Thresholds{})
	require.Contains(t, lag, "not trustworthy")
	require.Contains(t, lag, "--allow-lag")

	clamped := render(metronome.Snapshot{Count: 100, Clamped: 4, Max: 90 * time.Second},
		request.Thresholds{P99: secs(time.Second)})
	require.Contains(t, clamped, "not trustworthy")
	require.NotContains(t, clamped, "--allow-lag")
}

// Colour is a real option, not an accepted-and-ignored one.
func TestPrettyReportHonoursTheColorOption(t *testing.T) {
	r := sampleRun()
	var plain, coloured bytes.Buffer
	require.NoError(t, WriteReport(&plain, r, ReportOptions{Format: "pretty", Redactor: secret.NewRedactor(nil)}))
	require.NoError(t, WriteReport(&coloured, r, ReportOptions{Format: "pretty", Color: true, Redactor: secret.NewRedactor(nil)}))

	require.NotContains(t, plain.String(), "\x1b[")
	require.Contains(t, coloured.String(), "\x1b[")
	require.Contains(t, coloured.String(), "PASS")
}
