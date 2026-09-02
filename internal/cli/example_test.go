package cli

import (
	"context"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/RomanAgaltsev/quiver/examples/local/server/app"
)

// copyExampleCollection copies the shipped example's YAML into a temp tree.
// The files are verbatim — that is the point — but a run writes history, and a
// test must not write into the checked-in example.
func copyExampleCollection(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "..", "examples", "local")
	dst := t.TempDir()

	require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "server" || strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir // the Go server is not part of the collection
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	}))
	return dst
}

// The shipped example is the plan's declared acceptance test and the roadmap's
// Phase 0 exit criterion: "a multi-step auth-chained flow against the three
// protocols". Nothing ran it, so it could rot unnoticed — and it did: the
// example server exposed a /graphql endpoint no request file ever called.
//
// This runs the real collection files, in order, against the real example
// handlers, with -V pointing them at ephemeral ports.
func TestShippedExampleCollectionRuns(t *testing.T) {
	httpSrv := httptest.NewServer(app.NewHTTPHandler())
	defer httpSrv.Close()

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcSrv := grpc.NewServer()
	app.RegisterGRPC(grpcSrv)
	done := make(chan struct{})
	go func() { defer close(done); _ = grpcSrv.Serve(lis) }()
	t.Cleanup(func() {
		grpcSrv.Stop()
		<-done
	})

	collectionDir := copyExampleCollection(t)

	out, errOut, code := run(t,
		"run", filepath.Join(collectionDir, "requests"),
		"--collection", collectionDir,
		"--env", "dev",
		"-V", "base="+httpSrv.URL,
		"-V", "grpc_target="+lis.Addr().String(),
		"--quiet")

	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	// Every assertion in every request file must have been evaluated and passed.
	require.NotContains(t, errOut, "[FAIL]")
	for _, want := range []string{
		"[PASS] assertion[0]",  // 01-login: status is 200
		"[PASS] authenticated", // 02-me: the captured token was accepted
		"[PASS] has a name",
		"[PASS] no graphql errors", // 03-search
		"[PASS] hero resolved",
		"[PASS] status is OK", // 04-echo
		"[PASS] echoed back",
	} {
		require.Contains(t, errOut, want)
	}
}

// The example must also fail honestly: with the wrong token nothing chains, and
// the run has to report it rather than pass quietly.
func TestShippedExampleFailsWhenTheChainBreaks(t *testing.T) {
	httpSrv := httptest.NewServer(app.NewHTTPHandler())
	defer httpSrv.Close()

	collectionDir := copyExampleCollection(t)
	_, errOut, code := run(t,
		"run", filepath.Join(collectionDir, "requests", "02-me.yaml"),
		"--collection", collectionDir,
		"--env", "dev",
		"-V", "base="+httpSrv.URL,
		"-V", "token=wrong",
		"--quiet")

	require.Equal(t, 1, code)
	require.Contains(t, errOut, "[FAIL] authenticated")
}

// The shipped load example is held to the same standard as the shipped
// collection: something must run it. `qv load` has its own report, its own exit
// codes and its own resolve-once path, so a load example nobody executes rots in
// a way the `qv run` end-to-end test cannot notice — and it shipped broken once
// already, pointed at a /users endpoint the example server did not serve.
//
// --allow-lag is deliberate: schedule lag is a property of the machine running
// the test, not of the example, and a busy CI runner must not turn this into a
// flake. Every other verdict stays fatal.
func TestShippedLoadExampleRuns(t *testing.T) {
	httpSrv := httptest.NewServer(app.NewHTTPHandler())
	defer httpSrv.Close()

	collectionDir := copyExampleCollection(t)

	out, errOut, code := run(t,
		"load", filepath.Join(collectionDir, "load", "get-users.yaml"),
		"--collection", collectionDir,
		"--env", "dev",
		"-V", "base="+httpSrv.URL,
		"--setup", filepath.Join(collectionDir, "requests", "01-login.yaml"),
		"--rate", "500", "--requests", "100", "--allow-lag")

	require.Equal(t, 0, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out, "[PASS] p99")
	require.Contains(t, out, "[PASS] error_rate")
	// Raw and corrected percentiles are read as a pair; the report must never
	// print one without the other.
	require.Contains(t, out, "raw")
	require.Contains(t, out, "corrected")
	require.Contains(t, out, "100 requests", "the header must state the real stop condition")
}

// The load example must also fail honestly, and say why on stderr. With a bad
// token every response is a 401, so the declared error_rate threshold has to
// fail with exit 1 — and the reason has to be readable: this path once printed
// a formatter panic instead of a message, because the exitError was built
// around an error that was always nil.
func TestShippedLoadExampleFailsThresholdWithAReadableReason(t *testing.T) {
	httpSrv := httptest.NewServer(app.NewHTTPHandler())
	defer httpSrv.Close()

	collectionDir := copyExampleCollection(t)

	out, errOut, code := run(t,
		"load", filepath.Join(collectionDir, "load", "get-users.yaml"),
		"--collection", collectionDir,
		"--env", "dev",
		"-V", "base="+httpSrv.URL,
		"-V", "token=wrong",
		"--rate", "500", "--requests", "50", "--allow-lag")

	require.Equal(t, 1, code, "stdout:\n%s\nstderr:\n%s", out, errOut)
	require.Contains(t, out, "[FAIL] error_rate")
	require.Contains(t, errOut, "threshold failed")
	require.Contains(t, errOut, "error_rate")
	require.NotContains(t, errOut, "PANIC")
}
