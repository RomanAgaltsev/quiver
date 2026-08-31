package cli

import (
	"fmt"
	"os"
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
	History    *history.Store // nil under --dry-run: nothing may be written
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

// collectionRoot resolves the collection root for a target, honouring
// --collection and falling back sensibly when there is no collection.yaml.
func collectionRoot(f flags, target string) (string, error) {
	if f.collectionDir != "" {
		// An explicit --collection is an assertion by the user. A typo must say so
		// here rather than surfacing later as a wave of "unresolved variable"
		// errors against an empty collection.
		info, err := os.Stat(f.collectionDir)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("--collection %q is not a directory", f.collectionDir)
		}
		if _, err := os.Stat(filepath.Join(f.collectionDir, "collection.yaml")); err != nil {
			return "", fmt.Errorf("--collection %q contains no collection.yaml", f.collectionDir)
		}
		return f.collectionDir, nil
	}

	probe := target
	if probe == "" {
		probe = "."
	}
	if found, err := collection.FindRoot(probe); err == nil {
		return found, nil
	}
	// No collection.yaml is fine for ad-hoc commands and single-file runs: fall
	// back to the probe *directory*. filepath.Dir on a directory returns its
	// parent, which roots the collection one level too high — history then lands
	// in the wrong place and --env looks for the wrong file.
	if info, err := os.Stat(probe); err == nil && info.IsDir() {
		return probe, nil
	}
	return filepath.Dir(probe), nil
}

// environmentPath maps --env to a file. A value containing a separator is a
// path; anything else names a file in <root>/environments.
func environmentPath(root, name string) (string, error) {
	if strings.ContainsAny(name, `/\`) {
		return name, nil
	}
	// Request loading accepts .yaml and .yml, so environments must not be
	// stricter — writing .yml consistently used to load requests but not envs.
	var tried []string
	for _, ext := range []string{".yaml", ".yml"} {
		cand := filepath.Join(root, "environments", name+ext)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
		tried = append(tried, cand)
	}
	return "", fmt.Errorf("environment %q not found (looked for %s)", name, strings.Join(tried, " and "))
}

// newRunContext resolves the collection, environment, secrets, and executors.
// target may be "" for commands that operate on the collection itself.
func newRunContext(cmd *cobra.Command, target string) (*runContext, error) {
	f := readFlags(cmd)

	root, err := collectionRoot(f, target)
	if err != nil {
		return nil, err
	}

	col, err := collection.Load(root)
	if err != nil {
		return nil, err
	}

	var envVars map[string]string
	if f.envName != "" {
		path, pathErr := environmentPath(root, f.envName)
		if pathErr != nil {
			return nil, pathErr
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
	} else {
		// Attach it, so a {{env:NAME}} discovered inside a request file during
		// resolution is redacted by the render and history paths that were wired
		// up before that secret existed.
		resolved.Redactor = red
	}

	// Timeout precedence: a request's own `timeout:` (applied per call by the
	// executor) beats --timeout, which beats the collection default, which beats
	// the executor's built-in.
	timeout := f.timeout
	if timeout == 0 {
		timeout = col.Timeout.Duration()
	}

	// The HTTP executor is built once and shared with GraphQL, so
	// --insecure and --timeout reach both.
	httpExec := httpx.New(httpx.WithTimeout(timeout), httpx.WithInsecure(f.insecure))
	reg := core.Registry{
		request.ProtocolHTTP:    httpExec,
		request.ProtocolGRPC:    grpcx.New(grpcx.WithTimeout(timeout), grpcx.WithInsecure(f.insecure)),
		request.ProtocolGraphQL: graphqlx.New(httpExec),
	}

	// History lives under the collection root, not the process CWD — and is not
	// opened at all for a dry run, which must leave no trace on disk.
	var hist *history.Store
	if !f.dry {
		if hist, err = history.Open(filepath.Join(root, ".qv", "history"), red); err != nil {
			return nil, err
		}
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
			Warn:        cmd.ErrOrStderr(),
		},
	}, nil
}
