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
```

See [CONTRIBUTING.md](CONTRIBUTING.md) to get started and
[ROADMAP.md](ROADMAP.md) for post-MVP direction. Security reports go through
[SECURITY.md](SECURITY.md).

## Licence

Apache-2.0. See [LICENSE](LICENSE).
