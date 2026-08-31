package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/history"
	"github.com/RomanAgaltsev/quiver/internal/render"
	"github.com/RomanAgaltsev/quiver/internal/request"
)

// splitPairs parses repeated key/value flags. Headers and query params use
// different separators on purpose: one shared parser that tried ":" then "="
// mangled every query value containing a colon, e.g. a URL.
func splitPairs(items []string, sep, flag string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(items))
	for _, it := range items {
		k, v, ok := strings.Cut(it, sep)
		if !ok {
			// Silently dropping this would send a request missing the header the
			// user asked for — worse than failing.
			return nil, fmt.Errorf("%s %q must be key%svalue", flag, it, sep)
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

// adhocAuth builds an auth profile from the inline flags.
func adhocAuth(bearer, user string) (*request.AuthProfile, error) {
	switch {
	case bearer != "" && user != "":
		return nil, fmt.Errorf("--bearer and --user are mutually exclusive")
	case bearer != "":
		return &request.AuthProfile{Type: "bearer", Token: bearer}, nil
	case user != "":
		name, pass, _ := strings.Cut(user, ":")
		return &request.AuthProfile{Type: "basic", Username: name, Password: pass}, nil
	}
	return nil, nil
}

// expandAll applies environment/variable resolution to ad-hoc string arguments,
// so `qv http GET "{{base}}/users" --env dev` works.
func expandAll(rc *runContext, values ...*string) error {
	return rc.Resolved.ExpandAll(values...)
}

// expandMaps does the same for the repeatable key/value flags — headers, query
// params, metadata. These are arguments too, and the most valuable ad-hoc use of
// a variable is a token in a header, which is exactly what used to be skipped.
func expandMaps(rc *runContext, maps ...*map[string]string) error {
	for _, m := range maps {
		if m == nil || *m == nil {
			continue
		}
		out, err := rc.Resolved.ExpandMap(*m)
		if err != nil {
			return err
		}
		*m = out
	}
	return nil
}

// adhocName labels an unsaved request in the history log. It has no file to name
// it, so the call itself is the name.
func adhocName(rr core.ResolvedRequest) string {
	switch {
	case rr.HTTP != nil:
		url, err := rr.HTTP.EffectiveURL()
		if err != nil {
			url = rr.HTTP.URL
		}
		return fmt.Sprintf("%s %s", strings.ToUpper(rr.HTTP.Method), url)
	case rr.GRPC != nil:
		return fmt.Sprintf("%s @ %s", rr.GRPC.Method, rr.GRPC.Target)
	case rr.GraphQL != nil:
		return "POST " + rr.GraphQL.URL
	}
	return "ad-hoc"
}

// executeAdHoc runs one unsaved request and renders it.
func executeAdHoc(cmd *cobra.Command, rc *runContext, rr core.ResolvedRequest) error {
	defer func() { _ = rc.Registry.Close() }() // Q42

	if rc.RunOpts.DryRun { // Q40
		return render.DryRun(cmd.OutOrStdout(), &rr, rc.Render)
	}

	exec, ok := rc.Registry[rr.Protocol]
	if !ok {
		return configErr(fmt.Errorf("no executor for protocol %q", rr.Protocol))
	}
	resp, err := exec.Execute(cmd.Context(), rr)
	if err != nil {
		return classify(err)
	}

	// An ad-hoc call is exactly the kind of exploration history exists for, and
	// it is the only quiver activity nothing else on disk records. It has no
	// source file, so `history replay` refuses it by design.
	if rc.History != nil {
		if err := rc.History.Append(history.Record{
			ID:       history.NewID(),
			Time:     time.Now(),
			Name:     adhocName(rr),
			Protocol: string(rr.Protocol),
			Status:   resp.Status,
			OK:       resp.OK,
			Duration: resp.Duration.String(),
			Env:      rc.RunOpts.Env,
			Vars:     rc.RunOpts.Overrides,
		}); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "qv: warning: history not recorded: %v\n", err)
		}
	}

	if quiet, _ := cmd.Flags().GetBool("quiet"); !quiet {
		if err := render.Render(cmd.OutOrStdout(), resp, rc.Render); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout()); err != nil {
			return err
		}
	}
	if rc.RunOpts.FailOnError && !resp.OK {
		return &exitError{code: 1, err: fmt.Errorf("%s: non-OK response (--check-status)", adhocName(rr))}
	}
	return nil
}
