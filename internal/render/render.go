// Package render formats a response for terminal or machine consumption.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/secret"
)

// Options controls output. Redactor must never be nil in production; a nil
// Redactor is safe and redacts nothing (used by --show-secrets and by tests).
type Options struct {
	Format   string // "pretty" | "raw" | "json"
	Verbose  bool   // show headers and timings
	Color    bool   // resolved by ShouldColor
	Redactor *secret.Redactor
}

// ShouldColor reports whether to emit ANSI colour: only for a TTY, and never
// when NO_COLOR is set (https://no-color.org).
func ShouldColor(w io.Writer) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	f, ok := w.(*os.File)
	return ok && isatty.IsTerminal(f.Fd())
}

func Render(w io.Writer, resp *core.Response, opts Options) error {
	switch opts.Format {
	case "raw":
		_, err := w.Write(opts.Redactor.Bytes(resp.Body))
		return err
	case "json":
		return renderJSON(w, resp, opts)
	case "pretty":
		return renderPretty(w, resp, opts)
	default:
		return fmt.Errorf("render: unknown format %q (want pretty, raw, or json)", opts.Format)
	}
}

func renderJSON(w io.Writer, resp *core.Response, opts Options) error {
	out := map[string]any{
		"protocol": string(resp.Protocol),
		"status":   resp.Status,
		"ok":       resp.OK,
		"duration": resp.Duration.String(),
		"headers":  resp.Headers,
		"body":     json.RawMessage(bodyOrString(resp.Body)),
	}
	if resp.StatusText != "" {
		out["status_text"] = resp.StatusText
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	// Redact after encoding so header values and body alike are covered.
	_, err := w.Write(opts.Redactor.Bytes(buf.Bytes()))
	return err
}

func renderPretty(w io.Writer, resp *core.Response, opts Options) error {
	status := resp.StatusText
	if status == "" {
		status = fmt.Sprintf("%d", resp.Status)
	}
	statusText := status
	if opts.Color {
		c := color.New(color.FgGreen)
		if !resp.OK {
			c = color.New(color.FgRed)
		}
		statusText = c.Sprint(status)
	}
	if _, err := fmt.Fprintf(w, "%s  (%s)\n", statusText, resp.Duration); err != nil {
		return err
	}

	// Headers were previously never shown in pretty output at all, and
	// -v/--verbose was not implemented anywhere.
	if opts.Verbose && len(resp.Headers) > 0 {
		keys := make([]string, 0, len(resp.Headers))
		for k := range resp.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			line := fmt.Sprintf("%s: %s", k, strings.Join(resp.Headers[k], ", "))
			if _, err := fmt.Fprintln(w, opts.Redactor.String(line)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	var pretty bytes.Buffer
	if json.Valid(resp.Body) {
		if err := json.Indent(&pretty, resp.Body, "", "  "); err != nil {
			return err
		}
	} else {
		pretty.Write(resp.Body)
	}
	_, err := w.Write(opts.Redactor.Bytes(pretty.Bytes()))
	return err
}

// DryRun prints the fully resolved request without sending it.
//
// Template resolution spans four precedence layers plus chained captures; without
// this the user has no way to see what would actually be sent. It is redacted for
// the same reason history is: a resolved auth header is exactly what shows up here.
func DryRun(w io.Writer, rr *core.ResolvedRequest, opts Options) error {
	red := opts.Redactor
	line := func(format string, args ...any) error {
		_, err := fmt.Fprintln(w, red.String(strings.TrimRight(fmt.Sprintf(format, args...), "\n")))
		return err
	}

	writeHeaders := func(h map[string]string) error {
		keys := make([]string, 0, len(h))
		for k := range h {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := line("%s: %s", k, h[k]); err != nil {
				return err
			}
		}
		return nil
	}

	switch {
	case rr.HTTP != nil:
		if err := line("%s %s", strings.ToUpper(rr.HTTP.Method), rr.HTTP.URL); err != nil {
			return err
		}
		if err := writeHeaders(rr.HTTP.Headers); err != nil {
			return err
		}
		if len(rr.HTTP.Query) > 0 {
			if err := writeHeaders(rr.HTTP.Query); err != nil {
				return err
			}
		}
		if rr.HTTP.Body != "" {
			if err := line("\n%s", rr.HTTP.Body); err != nil {
				return err
			}
		}
	case rr.GRPC != nil:
		if err := line("gRPC %s %s", rr.GRPC.Target, rr.GRPC.Method); err != nil {
			return err
		}
		if err := writeHeaders(rr.GRPC.Metadata); err != nil {
			return err
		}
		if rr.GRPC.Message != "" {
			if err := line("\n%s", rr.GRPC.Message); err != nil {
				return err
			}
		}
	case rr.GraphQL != nil:
		if err := line("POST %s", rr.GraphQL.URL); err != nil {
			return err
		}
		if err := writeHeaders(rr.GraphQL.Headers); err != nil {
			return err
		}
		if err := line("\n%s", rr.GraphQL.Query); err != nil {
			return err
		}
		if rr.GraphQL.Variables != "" {
			if err := line("variables: %s", rr.GraphQL.Variables); err != nil {
				return err
			}
		}
	}
	if rr.Auth != nil {
		if err := line("(auth: %s)", rr.Auth.Type); err != nil {
			return err
		}
	}
	return nil
}

// bodyOrString returns the body as-is if it is valid JSON, otherwise as a JSON string.
func bodyOrString(body []byte) []byte {
	if json.Valid(body) {
		return body
	}
	quoted, _ := json.Marshal(string(body))
	return quoted
}
