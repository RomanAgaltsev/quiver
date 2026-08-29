package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/RomanAgaltsev/quiver/internal/collection"
	"github.com/RomanAgaltsev/quiver/internal/core"
	"github.com/RomanAgaltsev/quiver/internal/env"
	"github.com/RomanAgaltsev/quiver/internal/history"
	"github.com/RomanAgaltsev/quiver/internal/render"
	"github.com/RomanAgaltsev/quiver/internal/request"
	"github.com/RomanAgaltsev/quiver/internal/runner"
	"github.com/RomanAgaltsev/quiver/internal/secret"
	"github.com/RomanAgaltsev/quiver/internal/transport/graphqlx"
	"github.com/RomanAgaltsev/quiver/internal/transport/grpcx"
	"github.com/RomanAgaltsev/quiver/internal/transport/httpx"
)

// runContext is everything a command needs after flags are resolved.
type runContext struct {
	Collection *collection.Collection
	Resolved   *env.Resolved
	Redactor   *secret.Redactor
	Registry   core.Registry
	History    *history.Store
	Render     render.Options
	RunOpts    runner.Options
}

// flags reads the persistent flags. Cobra's typed getters cannot fail for flags
// we registered ourselves, so the errors are deliberately dropped.
type flags struct {
	output, envName, collectionDir string
	vars                           []string
	timeout                        time.Duration
	verbose, quiet, insecure       bool
	showSecrets, checkStatus, dry  bool
}

func readFlags(cmd *cobra.Command) flags {
	f := cmd.Flags()
	var out flags
	out.output, _ = f.GetString("output")
	out.envName, _ = f.GetString("env")
	out.collectionDir, _ = f.GetString("collection")
	out.vars, _ = f.GetStringArray("var")
	out.timeout, _ = f.GetDuration("timeout")
	out.verbose, _ = f.GetBool("verbose")
	out.quiet, _ = f.GetBool("quiet")
	out.insecure, _ = f.GetBool("insecure")
	out.showSecrets, _ = f.GetBool("show-secrets")
	out.checkStatus, _ = f.GetBool("check-status")
	out.dry, _ = f.GetBool("dry-run")
	return out
}

// newRunContext resolves the collection, environment, secrets, and executors.
// target may be "" for commands that operate on the collection itself.
func newRunContext(cmd *cobra.Command, target string) (*runContext, error) {
	f := readFlags(cmd)

	root := f.collectionDir
	if root == "" {
		probe := target
		if probe == "" {
			probe = "."
		}
		found, err := collection.FindRoot(probe)
		if err != nil {
			// No collection.yaml is fine for ad-hoc commands and single-file runs:
			// fall back to the probe directory rather than failing. Load tolerates a
			// missing collection.yaml, so this yields an empty collection.
			found = filepath.Dir(probe)
		}
		root = found
	}

	col, err := collection.Load(root)
	if err != nil {
		return nil, err
	}

	var envVars map[string]string
	if f.envName != "" {
		path := f.envName
		if !strings.ContainsAny(f.envName, `/\`) {
			path = filepath.Join(root, "environments", f.envName+".yaml")
		}
		if envVars, err = env.LoadEnvironment(path); err != nil {
			return nil, err
		}
	}

	overrides := map[string]string{}
	for _, kv := range f.vars {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--var %q must be key=value", kv)
		}
		overrides[k] = v
	}

	resolved, err := env.MergeVars(col.Defaults, envVars, overrides)
	if err != nil {
		return nil, err
	}

	// --show-secrets is implemented entirely by choosing the redactor (Q5).
	red := secret.NewRedactor(resolved.Secrets)
	if f.showSecrets {
		red = secret.NewRedactor(nil)
	}

	// The HTTP executor is built once and shared with GraphQL, so
	// --insecure and --timeout reach both.
	httpExec := httpx.New(httpx.WithTimeout(f.timeout), httpx.WithInsecure(f.insecure))
	reg := core.Registry{
		request.ProtocolHTTP:    httpExec,
		request.ProtocolGRPC:    grpcx.New(grpcx.WithTimeout(f.timeout), grpcx.WithInsecure(f.insecure)),
		request.ProtocolGraphQL: graphqlx.New(httpExec),
	}

	// History lives under the collection root, not the process CWD.
	hist, err := history.Open(filepath.Join(root, ".qv", "history"), red)
	if err != nil {
		return nil, err
	}

	return &runContext{
		Collection: col,
		Resolved:   resolved,
		Redactor:   red,
		Registry:   reg,
		History:    hist,
		Render: render.Options{
			Format:   f.output,
			Verbose:  f.verbose,
			Color:    render.ShouldColor(cmd.OutOrStdout()),
			Redactor: red,
		},
		RunOpts: runner.Options{
			FailOnError: f.checkStatus || col.FailOnError,
			DryRun:      f.dry,
			Env:         f.envName,
			Overrides:   overrides,
		},
	}, nil
}
