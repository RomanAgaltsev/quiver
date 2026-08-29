package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/env"
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
	for _, p := range values {
		if p == nil || *p == "" {
			continue
		}
		v, err := env.Expand(*p, rc.Resolved.Vars)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
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
		return runErr(err)
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
		return exitCodeErr(1)
	}
	return nil
}
