package request

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseHTTP(t *testing.T) {
	in := []byte(`
name: list users
protocol: http
order: 10
timeout: 5s
http:
  method: GET
  url: "{{base}}/users"
  query: { page: "2" }
captures:
  - var: token
    from: body
    path: data.token
assertions:
  - from: status
    op: eq
    value: "200"
`)
	r, err := Parse(in)
	require.NoError(t, err)
	require.Equal(t, ProtocolHTTP, r.Protocol)
	require.Equal(t, "GET", r.HTTP.Method)
	require.Equal(t, "2", r.HTTP.Query["page"])
	require.Equal(t, 10, *r.Order)
	require.Equal(t, 5*time.Second, r.Timeout.Duration())
	require.Len(t, r.Captures, 1)
	require.Equal(t, "token", r.Captures[0].Var)
	require.NoError(t, r.Validate())
}

// A mistyped key must be an error, not silence. `assertion:` instead of
// `assertions:` previously meant the checks never ran and the command exited 0.
func TestParseRejectsUnknownField(t *testing.T) {
	in := []byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\nassertion:\n  - from: status\n")
	_, err := Parse(in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "assertion")
}

// goccy's []byte unmarshal hook hands back re-serialized YAML *source*, so a
// duration's spelling and its position in the file change what the hook sees
// ("5s\n" mid-document, "\"5s\"" when quoted). Duration uses the
// InterfaceUnmarshaler form to avoid that; these pin every shape.
func TestParseTimeoutForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want time.Duration
	}{
		{"plain, mid-document", "timeout: 5s\nname: x\n", 5 * time.Second},
		{"double-quoted", "timeout: \"1500ms\"\nname: x\n", 1500 * time.Millisecond},
		{"single-quoted", "timeout: '2m'\nname: x\n", 2 * time.Minute},
		{"plain, last line, no trailing newline", "name: x\ntimeout: 90s", 90 * time.Second},
		{"absent", "name: x\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Parse([]byte(tc.in))
			require.NoError(t, err)
			require.Equal(t, tc.want, r.Timeout.Duration())
		})
	}
}

func TestParseRejectsBadTimeout(t *testing.T) {
	_, err := Parse([]byte("name: x\ntimeout: 5 seconds\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "timeout")
}

func TestValidateRejectsMissingBlock(t *testing.T) {
	r := &Request{Name: "x", Protocol: ProtocolGRPC} // no grpc block
	require.Error(t, r.Validate())
}

func TestValidateRejectsUnknownProtocol(t *testing.T) {
	r := &Request{Name: "x", Protocol: Protocol("ftp")}
	require.Error(t, r.Validate())
}

// A typo'd operator must be a config error (exit 2), not a silently failed
// assertion (exit 1) indistinguishable from a real API failure.
func TestValidateRejectsUnknownAssertionOp(t *testing.T) {
	r := &Request{
		Name: "x", Protocol: ProtocolHTTP,
		HTTP:       &HTTPSpec{Method: "GET", URL: "http://x"},
		Assertions: []Assertion{{From: "status", Op: "equals", Value: Val("200")}},
	}
	err := r.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "equals")
}

func TestValidateAssertionsAndCaptures(t *testing.T) {
	base := func() *Request {
		return &Request{Name: "x", Protocol: ProtocolHTTP, HTTP: &HTTPSpec{Method: "GET", URL: "http://x"}}
	}
	t.Run("capture needs a var", func(t *testing.T) {
		r := base()
		r.Captures = []Capture{{From: "status"}}
		require.Error(t, r.Validate())
	})
	t.Run("capture from body needs a path", func(t *testing.T) {
		r := base()
		r.Captures = []Capture{{Var: "v", From: "body"}}
		require.Error(t, r.Validate())
	})
	t.Run("unknown capture source", func(t *testing.T) {
		r := base()
		r.Captures = []Capture{{Var: "v", From: "trailer"}}
		require.Error(t, r.Validate())
	})
	t.Run("eq needs a value", func(t *testing.T) {
		r := base()
		r.Assertions = []Assertion{{From: "status", Op: "eq"}}
		require.Error(t, r.Validate())
	})
	t.Run("matches needs a valid regex", func(t *testing.T) {
		r := base()
		r.Assertions = []Assertion{{From: "body", Path: "n", Op: "matches", Value: Val("(")}}
		require.Error(t, r.Validate())
	})
	t.Run("not_exists is accepted", func(t *testing.T) { // Q11
		r := base()
		r.Assertions = []Assertion{{From: "body", Path: "errors", Op: "not_exists"}}
		require.NoError(t, r.Validate())
	})
}

// A copy-pasted block from another protocol is a mistake, not a no-op.
func TestValidateRejectsForeignBlock(t *testing.T) {
	r := &Request{
		Name: "x", Protocol: ProtocolHTTP,
		HTTP: &HTTPSpec{Method: "GET", URL: "http://x"},
		GRPC: &GRPCSpec{Target: "localhost:1", Method: "a.B/C"},
	}
	require.Error(t, r.Validate())
}

// A body may be inline or read from a file, never both.
func TestValidateRejectsBodyAndBodyFile(t *testing.T) {
	r := &Request{
		Name: "x", Protocol: ProtocolHTTP,
		HTTP: &HTTPSpec{Method: "POST", URL: "http://x", Body: "{}", BodyFile: "b.json"},
	}
	require.Error(t, r.Validate())
}

// `value: ""` asserts that a field is the empty string. Requiring a non-empty
// operand made that impossible to write; a *missing* key is still an error.
func TestValidateDistinguishesEmptyValueFromMissingValue(t *testing.T) {
	base := func(a Assertion) *Request {
		return &Request{Name: "r", Protocol: ProtocolHTTP,
			HTTP:       &HTTPSpec{Method: "GET", URL: "http://x"},
			Assertions: []Assertion{a}}
	}
	require.NoError(t, base(Assertion{From: "body", Path: "e", Op: "eq", Value: Val("")}).Validate())

	err := base(Assertion{From: "body", Path: "e", Op: "eq"}).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires a value")
}

func TestAuthProfileValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile AuthProfile
		wantErr bool
	}{
		{"bearer", AuthProfile{Type: "bearer", Token: "t"}, false},
		{"basic", AuthProfile{Type: "basic", Username: "u"}, false},
		{"apikey with header", AuthProfile{Type: "apikey", Header: "X-Key", Key: "k"}, false},
		{"apikey without header", AuthProfile{Type: "apikey", Key: "k"}, true},
		{"unknown type", AuthProfile{Type: "oauth2"}, true},
		{"empty type", AuthProfile{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.profile.Validate("main")
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// EffectiveURL is what both the executor and --dry-run use, so the preview and
// the wire cannot disagree.
func TestHTTPSpecEffectiveURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spec  HTTPSpec
		want  string
		isErr bool
	}{
		{"no query block leaves the string alone", HTTPSpec{URL: "https://x/y?b=2&a=1"}, "https://x/y?b=2&a=1", false},
		{"merges deterministically", HTTPSpec{URL: "https://x/y", Query: map[string]string{"b": "2", "a": "1"}}, "https://x/y?a=1&b=2", false},
		{"adds rather than replaces", HTTPSpec{URL: "https://x/y?t=a", Query: map[string]string{"t": "b"}}, "https://x/y?t=a&t=b", false},
		{"bad url", HTTPSpec{URL: "http://[::1]:namedport/"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.spec.EffectiveURL()
			if tc.isErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestOperandAndVal(t *testing.T) {
	require.Equal(t, "", Assertion{}.Operand(), "an unset value reads as empty")
	require.Equal(t, "x", Assertion{Value: Val("x")}.Operand())
}

func TestParseRejectsBothBodyAndBodyFileViaYAML(t *testing.T) {
	r, err := Parse([]byte("name: r\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" +
		"  body: \"{}\"\n  body_file: b.json\n"))
	require.NoError(t, err)
	require.ErrorContains(t, r.Validate(), "not both")
}

func TestValidateRejectsMissingName(t *testing.T) {
	r := &Request{Protocol: ProtocolHTTP, HTTP: &HTTPSpec{Method: "GET", URL: "http://x"}}
	require.ErrorContains(t, r.Validate(), "name is required")
}

func TestValidateRejectsIncompleteProtocolBlocks(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{"http without method", Request{Name: "r", Protocol: ProtocolHTTP, HTTP: &HTTPSpec{URL: "http://x"}}},
		{"grpc without block", Request{Name: "r", Protocol: ProtocolGRPC}},
		{"grpc without method", Request{Name: "r", Protocol: ProtocolGRPC, GRPC: &GRPCSpec{Target: "h:1"}}},
		{"graphql without block", Request{Name: "r", Protocol: ProtocolGraphQL}},
		{"graphql without query", Request{Name: "r", Protocol: ProtocolGraphQL, GraphQL: &GraphQLSpec{URL: "http://x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, tc.req.Validate())
		})
	}
}

func TestParseLoadSpec(t *testing.T) {
	in := []byte(`
name: get users
protocol: http
http:
  method: GET
  url: "{{base}}/users"
load:
  rate: 50
  duration: 30s
  concurrency: 20
  pacing: open
  weight: 3
  thresholds:
    p99: 250ms
    corrected_p99: 500ms
    error_rate: 0.01
    min_rps: 45
    max_schedule_lag: 100ms
`)
	r, err := Parse(in)
	require.NoError(t, err)
	require.NoError(t, r.Validate())

	require.NotNil(t, r.Load)
	require.Equal(t, 50.0, r.Load.Rate)
	require.Equal(t, 30*time.Second, r.Load.Duration.Duration())
	require.Equal(t, 20, r.Load.Concurrency)
	require.Equal(t, "open", r.Load.Pacing)
	require.Equal(t, 3, r.Load.Weight)

	th := r.Load.Thresholds
	require.NotNil(t, th)
	require.Equal(t, 250*time.Millisecond, th.P99.Duration())
	require.Equal(t, 500*time.Millisecond, th.CorrectedP99.Duration())
	require.NotNil(t, th.ErrorRate)
	require.InDelta(t, 0.01, *th.ErrorRate, 1e-9)
	require.NotNil(t, th.MinRPS)
	require.InDelta(t, 45.0, *th.MinRPS, 1e-9)
	require.Equal(t, 100*time.Millisecond, th.MaxScheduleLag.Duration())
}

func TestParseLoadRampAndPhases(t *testing.T) {
	ramp, err := Parse([]byte("name: r\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" +
		"load:\n  ramp: {start: 10, end: 100}\n  duration: 20s\n"))
	require.NoError(t, err)
	require.NotNil(t, ramp.Load.Ramp)
	require.Equal(t, 10.0, ramp.Load.Ramp.Start)
	require.Equal(t, 100.0, ramp.Load.Ramp.End)

	phased, err := Parse([]byte("name: p\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" +
		"load:\n  phases:\n    - {duration: 10s, rate: 10}\n    - {duration: 20s, rate: 50}\n"))
	require.NoError(t, err)
	require.Len(t, phased.Load.Phases, 2)
	require.Equal(t, 20*time.Second, phased.Load.Phases[1].Duration.Duration())
	require.Equal(t, 50.0, phased.Load.Phases[1].Rate)
}

// An unknown threshold key must be a config error, not a silently ignored
// setting — same strictness the MVP applies everywhere else.
func TestParseRejectsUnknownThresholdKey(t *testing.T) {
	_, err := Parse([]byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" +
		"load:\n  rate: 1\n  duration: 1s\n  thresholds:\n    p98: 10ms\n"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "p98")
}

// An unset error_rate threshold is different from `error_rate: 0`, which is why
// it is a pointer. A run with no error_rate declared must not require zero errors.
func TestThresholdZeroIsDistinctFromUnset(t *testing.T) {
	unset, err := Parse([]byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" +
		"load:\n  rate: 1\n  duration: 1s\n  thresholds:\n    p99: 10ms\n"))
	require.NoError(t, err)
	require.Nil(t, unset.Load.Thresholds.ErrorRate)

	zero, err := Parse([]byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" +
		"load:\n  rate: 1\n  duration: 1s\n  thresholds:\n    error_rate: 0\n"))
	require.NoError(t, err)
	require.NotNil(t, zero.Load.Thresholds.ErrorRate)
	require.Equal(t, 0.0, *zero.Load.Thresholds.ErrorRate)
}

func TestLoadSpecValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		yaml    string
		wantErr string
	}{
		"rate and ramp together": {
			"load:\n  rate: 10\n  ramp: {start: 1, end: 5}\n  duration: 5s\n", "exactly one"},
		"ramp without duration": {
			"load:\n  ramp: {start: 1, end: 5}\n  requests: 100\n", "duration"},
		"phases without durations": {
			"load:\n  phases:\n    - {rate: 10}\n  duration: 5s\n", "phases[0].duration"},
		"no stop condition": {
			"load:\n  rate: 10\n", "duration or requests"},
		"negative rate": {
			"load:\n  rate: -1\n  duration: 5s\n", "rate"},
		"zero concurrency is fine": {
			"load:\n  rate: 10\n  duration: 5s\n  concurrency: 0\n", ""},
		"negative concurrency": {
			"load:\n  rate: 10\n  duration: 5s\n  concurrency: -2\n", "concurrency"},
		"bad pacing": {
			"load:\n  rate: 10\n  duration: 5s\n  pacing: sideways\n", "pacing"},
		"negative weight": {
			"load:\n  rate: 10\n  duration: 5s\n  weight: -1\n", "weight"},
	} {
		t.Run(name, func(t *testing.T) {
			r, err := Parse([]byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n" + tc.yaml))
			require.NoError(t, err)
			err = r.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// A request with no load block is untouched — qv run must not care.
func TestRequestWithoutLoadBlockIsValid(t *testing.T) {
	r, err := Parse([]byte("name: x\nprotocol: http\nhttp:\n  method: GET\n  url: http://x\n"))
	require.NoError(t, err)
	require.Nil(t, r.Load)
	require.NoError(t, r.Validate())
}
