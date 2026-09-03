# quiver

`qv` — an open, CLI-first, git-friendly API client for HTTP, gRPC (unary), and
GraphQL, driven by plain-text YAML collections.

Requests live in your repository next to the code they exercise: they diff
cleanly, review naturally, and run anywhere the repo runs. Environments,
declarative captures, assertions, and a local append-only history make a
collection a test suite you can also explore interactively.

## Install

```sh
go install github.com/RomanAgaltsev/quiver/cmd/qv@latest
```

Or from a checkout: `task build` (produces `./qv`).

## Quick start

```sh
qv init my-api          # scaffold collection.yaml, environments/, requests/
cd my-api
qv new requests/ping.yaml --http
qv run requests/ping.yaml --env dev
```

The hermetic, fully offline example lives in
[`examples/local`](examples/local). It exercises all three protocols — an HTTP
login whose token is captured and chained, a GraphQL query, and a gRPC unary
call — with no network access:

```sh
go run ./examples/local/server &    # HTTP on :8080, gRPC on :50052
qv run examples/local/requests/ --env dev
```

## Collection layout

```
my-api/
  collection.yaml        # defaults, auth profiles, fail_on_error
  environments/
    dev.yaml             # plain-text values, committed
    prod.yaml
  requests/
    01-login.yaml        # one request per file; `order:` sequences folder runs
    02-me.yaml
  .qv/history/           # local history JSONL (gitignored)
```

`collection.yaml` is discovered by searching upward from the target, so
`qv run requests/anything.yaml` works from any directory inside the
collection. `--collection <dir>` overrides discovery.

```yaml
defaults:
  base: "https://api.example.com"

fail_on_error: true      # optional: exit 1 on any non-OK response

auth:
  main:
    type: bearer
    token: "{{token}}"   # resolved from the environment
```

## Requests

```yaml
name: login
protocol: http          # http | grpc | graphql
order: 10               # orders folder runs; unordered requests sort last
http:
  method: POST
  url: "{{base}}/login"
  headers:
    Content-Type: application/json
  body: '{"user":"ada"}'
captures:
  - var: token          # response -> variable, for later requests
    from: body          # body | header | status
    path: token         # gjson path (body) or header name
assertions:
  - name: status is 200
    from: status
    op: eq              # see Operators below
    value: "200"
auth: main              # reference a profile from collection.yaml
```

### Variables

`{{var}}` interpolates from the merged set of collection `defaults`, the
`--env` file, and `-V key=value` overrides (last wins). `{{env:NAME}}` resolves
from the process environment at run time — that is the secret reference
mechanism.

The `| json` filter JSON-escapes an interpolated value:

```yaml
body: '{"name":{{name | json}}}'
```

Without it, a captured value containing a quote, backslash, or newline would
produce invalid JSON.

### Captures

`captures` extract values from a response into variables that later requests
in the same run (and only that run) can interpolate:

```yaml
captures:
  - var: token
    from: body
    path: token
```

`from: header` reads `path` as a header name; `from: status` needs no path.

### Assertions

| Op           | Meaning                                  | Needs `value` |
|--------------|------------------------------------------|---------------|
| `eq`         | actual equals value                      | yes           |
| `ne`         | actual does not equal value              | yes           |
| `contains`   | actual contains value                    | yes           |
| `matches`    | actual matches value as a regex          | yes           |
| `exists`     | the path resolves at all                 | no            |
| `not_exists` | the path is absent                       | no            |

`not_exists` exists so a GraphQL response can be checked for a missing
`errors` key. A failed assertion fails the run (exit 1) and prints
`[FAIL] name — detail`.

Presence and emptiness are different: a field explicitly set to `""` *exists*.
Write `value: ""` to assert that it is empty; omitting `value:` entirely on an
operator that needs one is a config error.

For gRPC, `from: status` accepts the code number or its name — `"0"` and
`"OK"` are interchangeable, for every operator.

## Secrets and redaction

Anything resolved through `{{env:NAME}}` is a secret. It works anywhere a
template does — an environment file, `collection.yaml` defaults, or inline in a
request file — and the resolved value is redacted (`***`) in rendered output, in
`--dry-run`, and in history records.

`--show-secrets` disables redaction for debugging; never pipe its output
anywhere trusted. `qv env show --env dev` prints the resolved, redacted variable
set, which is the quickest way to verify the pipeline.

A template that cannot be resolved is a config error (exit 2), never text on the
wire. That includes an unset `{{env:NAME}}` and a variable whose *value* is
itself a template — substitution happens once and does not recurse.

## Ad-hoc requests

No file needed:

```sh
qv http GET https://api.example.com/users -H "Accept: application/json" -q page=2
qv http POST "$BASE/users" -d '{"name":"ada"}' --bearer "$TOKEN"
qv grpc localhost:50051 echo.Echo/Say -d '{"msg":"hi"}' --plaintext --bearer "$TOKEN"
qv graphql https://api.example.com/graphql -q '{ hero { name } }' --variables '{"ep":"JEDI"}'
```

`qv grpc` takes the target first, like `grpcurl`. (The reverse order is accepted
too — only one of the two arguments can contain a `/`.)

Ad-hoc commands resolve `--env`/`-V` variables in every argument, including
header, query and metadata *values*, so this works:

```sh
qv http GET "{{base}}/users" -H "Authorization: Bearer {{token}}" --env dev
```

`--bearer` and `--user` are mutually exclusive, and available on all three
commands. Ad-hoc calls are recorded in history like any other run; they have no
source file, so `qv history replay` declines them.

A request body sent with no explicit `Content-Type` gets one inferred from its
shape (`application/json` for JSON). Set the header yourself to override it.

## gRPC

Unary only in this release — no streaming RPCs anywhere.

```yaml
name: say
protocol: grpc
grpc:
  target: "localhost:50051"
  method: "echo.Echo/Say"
  message: '{"msg":"hi"}'
  # proto_files: [protos/echo.proto]   # local .proto instead of reflection
  # plaintext: true                    # skip TLS
```

With no `proto_files`, methods are resolved via server reflection. To add or
regenerate the test fixtures: `task proto` (needs `protoc`,
`protoc-gen-go`, `protoc-gen-go-grpc`).

`auth:` profiles work for gRPC as they do for HTTP: `bearer` and `basic` become
an `authorization` metadata entry, `apikey` becomes the header you name. An
explicit `metadata:` entry of the same key wins over the profile.

## Exit codes

| Code | Meaning                                                                            |
|------|------------------------------------------------------------------------------------|
| 0    | every request ran and every assertion passed                                       |
| 1    | transport failure, failed assertion, or non-OK response with `--check-status`      |
| 2    | configuration error (unknown env, bad YAML, unresolved variable, unknown auth, …)  |
| 3    | `qv load` only: the run completed but the measurement is not trustworthy           |

Exit 2 covers everything that is the *definition's* fault and means nothing was
sent — a malformed request file, an unresolved template, an auth profile that
does not exist, a `body_file` that cannot be read, a capture path that does not
resolve. Exit 1 means the request happened and the *result* was wrong. CI needs
that distinction, so quiver never collapses one into the other.

A dead target is exit 1 for every protocol: a gRPC call that never reached a
server is a transport failure, not a response to inspect.

A non-2xx HTTP status (or non-OK gRPC/GraphQL response) is a **normal,
inspectable response**, not a failure: exit 0 unless an assertion says
otherwise. `--check-status` (or `fail_on_error: true` in `collection.yaml`)
flips non-OK responses to exit 1.

### Per-protocol `status`

The normalized `status` differs by protocol, and `ok` is the
protocol-normalized success flag (gRPC `OK` is code `0`, which is ambiguous
on its own):

| Protocol | status            | ok                       |
|----------|-------------------|--------------------------|
| http     | HTTP status code  | 2xx                      |
| grpc     | numeric gRPC code | code == 0 (`OK`)         |
| graphql  | HTTP status       | 200 and no `errors` key  |

## Load testing

`qv load` promotes a saved request — or a folder of them — into a load test
against the same definition. Pacing, worker management and HDR statistics come
from [metronome](https://github.com/RomanAgaltsev/metronome); quiver supplies
the request, the thresholds and the exit code.

```sh
qv load requests/get-users.yaml --rate 200 --duration 30s
qv load requests/get-users.yaml --rate 200 --requests 5000 --progress
qv load requests/ --ramp 10:200 --duration 1m        # a folder, as a weighted mix
```

A run must be bounded: set `duration`, `requests`, or both (whichever is reached
first ends it). An unbounded load run is refused before anything is sent.

### The `--setup` chain

A load target may not declare `captures:`. Across thousands of concurrent
iterations there is no coherent answer to which response's captured value wins,
and silently ignoring the block would let you believe a chain is happening when
it is not — so it is a hard error, named, before a single request goes out.

Put the chain in a folder and point `--setup` at it instead:

```sh
qv load requests/get-users.yaml --setup requests/auth/
```

The setup folder runs **once**, through the same sequential runner `qv run`
uses — captures, assertions, `order:` and history all behave identically — and
its captured variables are in scope when the load target resolves. The target
resolves once, and the resolved request is shared read-only by every worker.

If the setup chain fails, no load is generated and the exit code is 1: those
requests were sent and the target refused them, which is not a config error.

### The `load:` block

```yaml
load:
  rate: 200               # requests per second (constant)
  ramp: {start: 10, end: 200}   # or: interpolate over the duration
  phases:                       # or: flat-rate segments
    - {duration: 30s, rate: 50}
    - {duration: 30s, rate: 200}
  duration: 30s           # at least one of duration/requests is required
  requests: 5000
  concurrency: 50         # max in flight; 0 uses metronome's default
  pacing: open            # open (default) | closed
  weight: 3               # folder targets only: share of the mix
  assertions: true        # run the request's assertions per iteration (default)
  thresholds:
    p50: 20ms
    p95: 80ms
    p99: 250ms
    corrected_p50: 40ms
    corrected_p95: 150ms
    corrected_p99: 500ms
    error_rate: 0.01      # 0..1, the TARGET's rate (see below)
    min_rps: 180
    max_schedule_lag: 50ms
```

Set exactly one of `rate`, `ramp` and `phases`. Every field has a flag that
overrides it for one run: `--rate`, `--ramp 10:200`, `--duration`, `--requests`,
`--concurrency`, `--pacing`. The file declares the durable contract; a flag is
the one-off. A `--ramp` or `--rate` override replaces whatever shape the file
declared, so "exactly one shape" still holds after the overlay.

In a folder target the run shape comes from the **first** request's `load:`
block and is shared by all of them; `weight` is the only per-file knob, and it
becomes that request's share of the mix. `order:` is meaningless under load and
is ignored — a mix has no sequence.

Any other `load:` key on a later request is a **config error** (exit 2), naming
the file and the keys. It would otherwise be ignored — and an ignored
`thresholds:` block means a run goes green having asserted nothing its author
asked for.

### Reading the report

```
requests    6000      errors 3 (0.05%)     saturated 0
achieved    199.8/s   throughput 1.2 MB/s

latency               p50      p95      p99      max
  raw                12ms     34ms     48ms     91ms
  corrected          12ms     35ms     52ms       —

schedule lag    max 3ms  (budget 50ms)   OK
```

Raw and corrected percentiles are always printed as a **pair**, and are read
together. Corrected percentiles add the time each unit spent waiting past its
scheduled send time, answering "what would a client that kept to the schedule
have seen?" A large gap between the two rows means the generator queued and the
raw numbers understate what a real client would have suffered.

**What counts as a failed iteration.** When the request declares `assertions:`,
they decide — exactly as they do under `qv run`, and including for a non-2xx
response. So a request asserting `status eq 404` load-tests an error path
without reporting a 100% error rate. When it declares none, a non-OK response is
the failure, since there is no other success signal. Set `assertions: false` in
the `load:` block to skip them and fall back to that.

`errors` and the `error_rate` threshold both exclude **saturation** — open-loop
units that found no free worker at their scheduled time. Those never reached the
target, so they belong in neither the numerator nor the denominator; counting
them would blame the target for the generator running out of workers.
`saturated` is reported on its own line, and metronome's own raw
`snapshot.error_rate` (which does include it) is in the JSON output.

For the same reason, **`min_rps` judges the rate that reached the target**, not
the rate the generator recorded. A saturated unit is still a recorded result, so
the raw rate counts requests that were never sent; when the two differ the
report shows both, and the JSON carries `attempted` and `attempted_rps`.

`-o json` prints the whole snapshot, both verdict lists and `exit_code` for a CI
job to consume. Secrets are redacted in both formats.

### Exit codes and trust

| Code | Meaning |
|------|---------|
| 0    | every declared threshold passed (with none declared, a run that measured something passes) |
| 1    | the **target** failed a threshold, or the `--setup` chain failed |
| 2    | config error — a bad profile, captures on the target, an unresolved variable; nothing was sent |
| 3    | the run completed but the **measurement** is not trustworthy |

Exit 3 exists because "the target is too slow" and "quiver could not generate
the load" are different failures with different fixes, and collapsing them would
throw away the signal metronome exists to surface. When both apply, 3 wins: a
verdict derived from numbers that do not describe the target is worthless.

Three things trigger it:

- **`saturation`** — more than 10% of units never found a free worker, so they
  were never sent. The percentiles, the error rate and the achieved rate then
  describe a population smaller than the run claims to have driven, and past
  that share they stop describing the target at all. A run in which *every* unit
  saturated measured nothing, whatever its thresholds say.

  Fix it the same way as schedule lag: lower the rate, raise `--concurrency`, or
  switch to `--pacing closed`. **`--allow-lag` does not waive this** — it waives
  `schedule_lag` alone, and "these numbers do not describe the target" must not
  become a silent exit 0.

- **`schedule_lag`** — the generator fell further behind its own schedule than
  the budget allows. The budget is `max(25ms, 5 send intervals)` unless
  `max_schedule_lag` declares one. This is only ever checked under `--pacing
  open`, where a busy target cannot cause lag: metronome emits a unit that finds
  no free worker immediately as saturated rather than delaying it, so lag there
  is the dispatcher, not the target. Under `--pacing closed` a worker does not
  ask for its next token until the current unit finishes, so lag *is* the target
  slowing down — absorbed as rate sag and judged by `min_rps` and the corrected
  percentiles instead.

  Fix it by lowering the rate, raising `--concurrency`, or switching to
  `--pacing closed`. metronome's open-loop dispatcher tops out in the low
  thousands of requests per second on one machine. `--allow-lag` downgrades this
  one verdict to a warning for known-noisy CI runners — it still prints, it just
  stops being fatal.

- **`histogram_range`** — results exceeded the histogram's 1m ceiling, so
  `p50`/`p95`/`p99` understate reality and any percentile threshold is an
  assertion about a number that is not real. `--allow-lag` deliberately does
  *not* waive this one. Latencies below the 1µs floor are clamped too, but that
  is harmless and is not reported: the value is recorded *as* the floor, which
  rounds percentiles up, and it happens on every fast loopback target and on any
  host whose monotonic clock is coarser than a microsecond.

### Progress

`--progress` prints a line per interval to **stderr**, keeping stdout
machine-readable; `--progress-interval` sets the cadence (default 1s). It shows
inter-tick deltas — count, errors, achieved rate — and deliberately no live
percentiles and no live lag. Every field metronome exposes mid-run is
cumulative, so a live percentile would be a lifetime figure presented as a
current one, and one early stall would pin lag red for the rest of the run.
Those arrive when metronome ships rolling-window stats.

### Dependency pin

metronome is pinned to an **exact** version, not a range. It is pre-v1 and its
own roadmap states that minor versions may carry breaking changes; a range would
be a silent upgrade of the thing that decides what the numbers mean. CI has a
job that fails if the pin drifts or the module stops building from a clean
module cache.

## Output

`-o/--output` selects `pretty` (default; syntax-highlighted JSON and a coloured
status line when on a TTY, honouring `NO_COLOR`), `raw` (body bytes verbatim),
or `json` (the full normalized response). `-v/--verbose` adds response headers
and timings; `--quiet` suppresses output entirely and reports only the exit code.

`--dry-run` prints the resolved request instead of sending anything — including
the URL with `query:` merged in, so the preview is exactly what would go on the
wire. It writes nothing to disk.

## History

Every run — saved or ad-hoc — records to `.qv/history/history.jsonl` under the
collection root: time-sortable IDs, the source request path (what replay
re-runs), the environment used, and redacted `-V` overrides. The file is created
on first write, so a `--dry-run` leaves no trace.

```sh
qv history list          # recent requests
qv history replay <id>   # re-run a record's source file with current env/vars
```

`replay` re-resolves the request from disk rather than replaying a frozen copy,
so it reflects the current file and environment. Ad-hoc records have no source
file and are declined.

## Development

```sh
task ci           # the local gate: fmt, lint, test, vuln
task build        # build ./qv with the version stamped in
task test         # go test -shuffle=on ./...
task test:race    # what CI runs; -race needs a C toolchain on Windows
task lint         # golangci-lint run
task fmt          # golangci-lint fmt
task cover        # coverage report
task vuln         # govulncheck
task proto        # regenerate gRPC test fixtures
task example      # run the hermetic example end to end
task example:load # run the hermetic load example (server must be up)
```

See [CONTRIBUTING.md](CONTRIBUTING.md) to get started and
[ROADMAP.md](ROADMAP.md) for post-MVP direction. Security reports go through
[SECURITY.md](SECURITY.md).

## Licence

Apache-2.0. See [LICENSE](LICENSE).
