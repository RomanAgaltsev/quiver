# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.4.0](https://github.com/RomanAgaltsev/quiver/compare/v1.3.0...v1.4.0) (2026-09-14)


### Features

* qv gen proto — generate a gRPC collection from proto files or reflection ([#23](https://github.com/RomanAgaltsev/quiver/issues/23)) ([9853a3b](https://github.com/RomanAgaltsev/quiver/commit/9853a3b8315b034c93dbeba1e97496c91a4e646a))

#### `qv gen proto` — a service definition becomes a runnable gRPC collection

```sh
qv gen proto ./api/*.proto --target localhost:50051 -o ./my-api   # from files
qv gen proto --reflect localhost:50051 --plaintext -o ./my-api    # from a live server
```

One request file per **unary** RPC, grouped by service, plus a `collection.yaml` carrying the
target as `{{grpc_target}}`. Descriptors come from `.proto` files or from a live server's
reflection service — never both in one run. With files, `--target` says where to call, because
a `.proto` describes *what* to call and never *where*.

**No new dependency.** `internal/transport/grpcx` already compiled `.proto` files and already
negotiated v1-vs-v1alpha reflection for `qv grpc`; what was missing was only *enumeration*,
since both existing paths resolve a single method by name. Both sources now produce
`grpcx.MethodInfo` through one shared walk, so the generator never branches on where a service
definition came from — and a test asserts the two enumerators classify the same service
identically, because a method called streaming by one and unary by the other would surface only
as a request file that fails at send time.

#### Generated messages are protojson, which is not what your .proto looks like

```yaml
name: PetStore/GetPet
protocol: grpc
grpc:
  target: "{{grpc_target}}"
  method: pkg.PetStore/GetPet
  message: |-
    {
      "asOf": "",
      "includePhotos": false,
      "petId": ""
    }
assertions:
  - name: ok
    from: status
    op: eq
    value: OK
```

A proto `pet_id` is sent as `petId`. The name is taken from the descriptor rather than
transformed by hand, so protojson's own rules apply — including its handling of names that
already contain capitals. Two consequences of the same kind, both worth knowing before you
compare a generated file against its `.proto`:

- A **64-bit integer is a JSON string** (`"0"`, not `0`), because a JSON number cannot hold one
  exactly.
- A **well-known type is its protojson form**: a `Timestamp` is an RFC 3339 string, generated as
  `""` rather than `{"seconds":0,"nanos":0}`.

Every one of these produces a file that YAML parses, JSON parses and a golden test happily pins
— and that the server rejects. That is why the release is gated on a test that generates from
the example server's own reflection service and then executes the result with the real
`qv run`, rather than on the suite alone.

proto3 has no `required`, so unlike OpenAPI generation there is nothing to filter on and every
field is emitted. `--depth` bounds nesting, and a **self-referential message terminates
regardless of `--depth`**: the cycle guard is a correctness control kept separate from the size
cap, so raising the cap cannot reintroduce a hang.

#### Streaming RPCs are skipped, and said so

quiver is unary-only until Phase 7. A client-, server- or bidi-streaming RPC is not generated,
and each is named in the report:

```
generated 2 file(s)

notes:
  - pkg.PetStore/ListPets is a streaming RPC and was skipped; quiver is unary-only until Phase 7
```

A streaming-heavy service produces a smaller collection than its `.proto` suggests. Saying which
RPCs went missing is the difference between a limitation and an apparent bug.

#### Re-generation, and where drift detection is weaker

2a's lockfile, writer, report, `--force` and `--check` are reused unchanged: a collection may
hold files from both generators and behaves the same either way. Your edits are skipped and
reported, never overwritten without `--force`.

One honest gap. A `--reflect` source records `source: reflect://host:port` and **no
`source_hash`**, because a live server has no stable bytes to fingerprint. Inventing one —
hashing the enumerated method list, say — would make every deployment that merely reordered its
services look like drift. So `--check` against a reflective target compares the generated files,
not the source. Against `.proto` files it compares both.

`grpc.proto_files` is resolved relative to the **request file**, so generated paths climb out of
the collection. When the output directory and the `.proto` sit on different Windows volumes there
is no relative path between them at all, and an absolute one is written instead; `qv run` honours
both.

#### For the next generator

Phase 2c (GraphQL) is the last of Phase 2 and inherits this lockfile design and these output
conventions. The lesson 2b adds to 2a's: **for any wire format with its own JSON mapping,
enumerate that mapping's type rules explicitly rather than assuming "the zero value for the
kind" is well defined.** Both of this release's near-misses — field-name casing and 64-bit
integers — were that assumption, and neither is visible to any check that stops at "it parses".

## [1.3.0](https://github.com/RomanAgaltsev/quiver/compare/v1.2.0...v1.3.0) (2026-09-14)


### Features

* qv gen openapi — generate a collection from an OpenAPI spec ([#21](https://github.com/RomanAgaltsev/quiver/issues/21)) ([e70592e](https://github.com/RomanAgaltsev/quiver/commit/e70592ea8a0bfdd8daa09763db920c96d3aff18b))

#### `qv gen openapi` — a spec becomes a collection you can run, and re-run

```sh
qv gen openapi ./openapi.yaml -o ./my-api
```

Adopting quiver used to mean authoring a request file per endpoint by hand. For an API of any
size that is the entire cost of switching, paid up front, before any of quiver's advantages are
felt. Every such API already has a machine-readable description, so now it writes the collection.

One request file per operation, grouped by the operation's first tag — the grouping the spec's
own author already chose — plus a `collection.yaml` carrying `servers[0]` as `defaults.base` and
`securitySchemes` as auth profiles. OpenAPI **3.x only**; a Swagger 2.0 document is refused by
name rather than half-mapped into something quietly wrong.

#### Generated files are runnable, not drafts

The mapping that makes the difference is that **path parameters become quiver templates**:

```yaml
name: getPetById
protocol: http
http:
  method: GET
  url: "{{base}}/pets/{{petId}}"
auth: bearerAuth
assertions:
  - name: ok
    from: status
    op: eq
    value: "200"
```

`qv run ./my-api -V petId=42` works on the file as generated. Only *required* query and header
parameters are emitted, or optional ones the spec gives an `example` or `default` for — emitting
every optional parameter would bury the two that matter. No `timeout:` is generated, because a
per-operation timeout invented from a spec is a guess.

**Every generated request asserts something**: the lowest declared 2xx status, so a generated
collection is a CI gate on its first run rather than a set of stubs someone has to finish. An
operation declaring no 2xx asserts that the status is under 400 instead, and says so in the
report.

That claim is tested the only way it can honestly be made: the end-to-end test generates a
collection from a spec describing `examples/local/server` and executes it with the real `qv run`,
asserting the run comes back green — and a second test proves it fails when the token is wrong.
A file that looks plausible and that no executor accepts is exactly the failure golden tests
cannot see.

#### No credential is ever written to a file

`securitySchemes` become auth profiles whose every credential is an `{{env:...}}` reference —
`{{env:BEARERAUTH_TOKEN}}`, `_USERNAME`/`_PASSWORD`, `_KEY`. There is a test asserting that no
literal credential from a spec's own examples reaches disk, for every scheme kind. A generator
that wrote a placeholder secret into a git-diffable file would be teaching the wrong habit at the
first moment a user sees its output.

`oauth2` and `openIdConnect` cannot be performed yet (Phase 7). They are emitted as a commented
stub and named in the report rather than silently dropped, so a collection that cannot
authenticate says why.

#### Re-generation keeps your edits, and says what it kept

The point of a generator is undermined if its output cannot be re-generated after the spec moves.
`.qv/gen.lock` records what `qv gen` wrote and what each file looked like when it wrote it:

| the lock says | the file on disk | what happens |
|---|---|---|
| absent | absent | written — a new operation |
| present | unchanged since generation | rewritten from the spec |
| present | **you edited it** | **skipped**, and reported |
| present | you deleted it | written back, and reported |
| absent | exists | skipped — qv did not write it |

**A file you have edited is never overwritten without `--force`**, and every skip is printed. An
operation that leaves the spec is reported as *orphaned* and left on disk: deleting a file because
an endpoint disappeared is the user's decision, not the tool's.

There is no three-way merge, deliberately. Merging YAML someone has restructured cannot be done
correctly without a model of their intent, and a merge that is *usually* right is the worst option
available — it corrupts quietly, in the files that are their source of truth, and git shows the
damage only if they look. Skipping is always correct and always visible; the lockfile is what
makes the skip precise rather than conservative.

**Commit `.qv/gen.lock`.** It is the opposite case from `.qv/history/`, which is local and
gitignored, and the shared `.qv/` prefix makes the wrong assumption the natural one. Without it, a
re-run treats every existing file as unmanaged and touches nothing.

#### `--check` for CI

`qv gen openapi spec.yaml -o ./my-api --check` runs the whole generation in memory through the
same code path as a real run, prints what would change, writes nothing, and exits **1** if the
committed collection has drifted from its spec. Files you have deliberately edited are reported
but are not drift — keeping them is the promise, not a failure.

Exit codes stay honest: **0** generated, skips included; **2** for an unreadable spec, a 2.0
document, or an unwritable output directory; and **1** only for `--check` drift. Generation has no
notion of a failing assertion, and reusing 1 would blur the code that means "the API under test
misbehaved".

#### Notes for the next generator

Phase 2b (proto) and 2c (GraphQL) inherit this lockfile design and these output conventions.
Three things this release learned the hard way and they should not relearn:

- **libopenapi refuses a self-referential schema at load time.** `Pet.friend: Pet` made
  `BuildV3Model` fail outright, so the document could not be read at all. The circular-reference
  check is skipped and the skeleton walk carries its own path, stopping at the first repeat with
  `{}`. A depth cap alone was not enough — it produced a body nested five Pets deep.
- **A library that logs is a library that pollutes your report.** libopenapi writes JSON log lines
  to stderr through slog by default; it is handed a discarding logger.
- **Read the goldens.** The first pass emitted every JSON body as an escaped single-line scalar.
  It round-tripped perfectly and was unreadable, which fails the requirement that actually matters:
  files a person is happy to own.

## [1.2.0](https://github.com/RomanAgaltsev/quiver/compare/v1.1.1...v1.2.0) (2026-09-14)


### Features

* adopt metronome v0.9.0 — live percentiles, per-endpoint breakdown, warmup ([#17](https://github.com/RomanAgaltsev/quiver/issues/17)) ([a2d5b90](https://github.com/RomanAgaltsev/quiver/commit/a2d5b902f900ad2bc52c4fe3d7394ee650ee417a))

#### Adopting metronome v0.9.0 — live percentiles, a per-request breakdown, and `--warmup`

The pin moves from **v0.4.0 to v0.9.0**, five releases, two of which exist because
quiver asked for them. Nothing quiver compiled against changed meaning; what the bump
buys is three things v1.1.0 deferred with a reason, each of which is now shipped.

#### `--progress` prints live percentiles, because it finally can

The progress line used to print count, errors and a rate quiver derived itself, and
**deliberately no percentiles and no lag**: every field metronome exposed mid-run was
cumulative, so a live p99 would have been a lifetime figure presented as a current one
and one early stall would have pinned lag red for the rest of the run. quiver declined
to print a number it could not stand behind.

```
    3s     198 reqs     0 err     198.0/s   p50 11ms     p99 47ms     lag 2ms
```

Every figure is now a trailing window over the last ten intervals, read from a
`RollingStats` fanned alongside the reported aggregate on the same histogram range —
a different range would make `Window().P99` and `Snapshot().P99` disagree for no
visible reason. The refusal is gone because the reason for it is.

#### A folder target reports per request

A folder drives several requests through one `Mix`, and one p99 over all of them
describes none of them. quiver has stamped `Labels{"request": name}` on every Result
since v1.1.0 and nothing could read it back until metronome v0.6.

```
per request         reqs      err      p50      p99
  list                 75        0      9ms     20ms
  search               25        0    180ms    400ms
```

JSON gains a `by_request` array with the same fields plus `clamped` and
`corrected_clamped` **per series** — one slow endpoint clamping its own histogram
while the total is fine is the likelier shape.

A single-target run prints no such section and **does not build the aggregate at
all**. Every recorder runs on the goroutine draining the result channel, so an
aggregate nothing reads is latency added to the generator; this was found the hard
way, as a flaky test that only failed under load.

#### `--warmup` excludes a cold start from the report, not from the load

Cold connection pools, TLS handshakes that will be reused and an unwarmed target all
land in the histogram a `p99` threshold is judged against, and none of them is the
system under test. `--warmup 5s`, or `warmup: 5s` in the `load:` block, keeps that
prefix out of the report while still sending it — and `--progress` still shows it,
because a progress line printing zeros while the pool warms looks like a hung run.

```
measured        4500 of 5000 requests  (5s warmup excluded)
```

`count + skipped` is the whole population, in the text report and in JSON. An
exclusion that cannot be audited is a number that quietly shrank. A warmup at or over
the run's `duration` is rejected at config time.

**The trust verdict now reads the warmed population too**, which needed no new code:
the verdict reads the snapshot it is handed, and the warmup changed which population
that snapshot describes. A cold start that made the generator briefly late no longer
fails a run as untrustworthy.

#### Fixed: a weight-only `load:` block was rejected

A folder member declaring only `weight` — the documented form, explicitly permitted by
the rule that rejects a later file declaring anything *else* — failed validation with
"set exactly one of rate, ramp, or phases". **A two-request load folder could not be
written at all.** Found by running the new example rather than by reading the code,
which is the same lesson amendment A7 recorded: an example nothing runs is an example
that has already broken.

#### Fixed: the clamp marker flagged healthy runs

The new per-series clamp marker fired on *any* clamping, including the low-side clamp
that rounds a sub-microsecond latency up to the histogram floor. That cannot hide a
slow request, and a handful of them is normal on a localhost run — so a healthy run
was marked "percentiles understate". It now discriminates on `Max > statsHigh`, the
same rule the trust verdict already used, and the CI example asserts a green run
carries no marker.

#### Also

- A two-request hermetic example, `examples/local/load-folder/`, exercising the
  breakdown, weights and warmup. CI runs it and asserts all three, the way
  amendment A7 established.
- `golang.org/x/time` to v0.16.0, required by metronome v0.9.0 rather than chosen here.
- **The `go` directive moves to `1.27`**, and the minor-only form is deliberate. Every
  workflow resolves its toolchain from `go-version-file: go.mod`, and `actions/setup-go`
  treats a bare minor as "the latest patch of that line" but an explicit patch as an exact
  pin. `go get` had canonicalised the directive to `1.26.0`, which pinned CI to the initial
  go1.26 release and lit up **23 standard-library advisories** in `govulncheck`, every one
  of them already fixed in go1.26.1. Writing `1.27.1` here would have bought the same trap
  back the day a fix lands in 1.27.2. **quiver now needs Go 1.27 or newer to build.**

## [1.1.1](https://github.com/RomanAgaltsev/quiver/compare/v1.1.0...v1.1.1) (2026-09-03)


### Bug Fixes

* all five findings of the load-testing implementation review ([#10](https://github.com/RomanAgaltsev/quiver/issues/10)) ([2e09e66](https://github.com/RomanAgaltsev/quiver/commit/2e09e666015d30b48dbcdf503d95b114350c32a1))

#### `qv load` — three behaviour changes worth knowing before you upgrade

All three are **loud**: each one changes an exit code at a point you see immediately, and
none quietly turns a passing run into a differently-passing one. Two convert a wrong
outcome into a right one; the third only loosens.

- **A run in which most units never reached the target now exits 3.** Open-loop units
  that find no free worker are recorded as results but are never sent, so the
  percentiles, the error rate and the achieved rate all describe a smaller population
  than the run claims to have driven. Above a **10% saturated share** that is now a
  `saturation` trust verdict. Previously a run in which *every single unit* saturated
  exited **0** with every threshold passing — `error_rate` read 0% because there were no
  attempts to divide by, and `min_rps` reported the *offered* rate as achieved.
  **`--allow-lag` does not waive this**; it waives `schedule_lag` alone.
- **`min_rps` now judges the rate that reached the target**, not the rate the generator
  recorded. A saturated unit is still a recorded result, so the raw rate counts requests
  that were never sent. When the two differ the report shows both, and `-o json` carries
  `attempted` and `attempted_rps`.
- **A request's assertions now decide a load iteration, including on a non-2xx
  response.** Previously a non-OK response was an error *and* short-circuited the
  assertions, so a request asserting `status eq 404` reported a 100% error rate and its
  assertion was never evaluated — and the same file meant different things under
  `qv run` and `qv load`. With no assertions declared, a non-OK response is still the
  failure, which is the common case and is unchanged.

#### `qv load` — one new config error

- **A `load:` key other than `weight` on any but the first request of a folder target is
  now a config error (exit 2)**, naming the file and the keys. A folder shares one run
  shape, taken from the first request's block, and `weight` is the only per-file knob —
  so such a block was previously *half*-honoured: its weight applied and its
  `thresholds:` vanished without a word. If this fails a collection that used to run, it
  was a collection whose thresholds were never being enforced.

#### Also

- `Result.Start` is taken from the injected clock rather than the wall clock, so a run
  driven by a `ManualClock` no longer infers its rate figures from real elapsed time.

Full review, including why the saturation verdict is a *share* rather than any saturation
at all: `quiver/reviews/2026-09-03-load-testing-implementation-review.md` in the design
vault. This release fixes every finding of it.

## [1.1.0](https://github.com/RomanAgaltsev/quiver/compare/v1.0.0...v1.1.0) (2026-09-02)


### Features

* load testing ([#8](https://github.com/RomanAgaltsev/quiver/issues/8)) ([e9fa0ac](https://github.com/RomanAgaltsev/quiver/commit/e9fa0ac787c783d762068d336930ce2a9583a8eb))

## 1.0.0 (2026-08-31)


### Features

* **assert:** declarative response assertions ([90126dc](https://github.com/RomanAgaltsev/quiver/commit/90126dcf8cc6f35a2ef82624f18c5d0ef0401f67))
* **capture:** declarative response-to-variable extraction ([6f8babe](https://github.com/RomanAgaltsev/quiver/commit/6f8babe1d36828480e61d26eff284f1c7a8605c1))
* **cli:** ad-hoc http/grpc/graphql with env resolution, auth, correct ([4584db0](https://github.com/RomanAgaltsev/quiver/commit/4584db037c0da2947ae10cf36b7aeddc63778424))
* **cli:** run command, shared run context, exit codes, redaction, ([1b2e2e0](https://github.com/RomanAgaltsev/quiver/commit/1b2e2e0284736803ac00877077f4e9aa4eb03f91))
* **collection:** load collection.yaml, bounded root discovery, ordered ([5d4bd74](https://github.com/RomanAgaltsev/quiver/commit/5d4bd741c6c1c4f1631d8d42140fbdbc78e9dd15))
* **core:** normalized Response, ResolvedRequest, Executor, Closer, ([0f72f5f](https://github.com/RomanAgaltsev/quiver/commit/0f72f5feaa8397dc5130029b381a588d8b7d6bc0))
* **env:** variable merge, secret refs, templating, request resolution ([25bc77b](https://github.com/RomanAgaltsev/quiver/commit/25bc77b88203d294a190995c84941b47e0273541))
* **graphqlx:** GraphQL executor over an injected HTTP transport, with ([d3648ac](https://github.com/RomanAgaltsev/quiver/commit/d3648ace216e1735c322688d23aa5e998c3a9f84))
* **grpcx:** dynamic gRPC unary executor with reflection, TLS, metadata, ([62f3fa6](https://github.com/RomanAgaltsev/quiver/commit/62f3fa6e28df43256b68dccb95f42683e475355a))
* **grpcx:** resolve gRPC methods from local .proto files via ([b4b20de](https://github.com/RomanAgaltsev/quiver/commit/b4b20deb6c33ab98c54fbc4823a25f503bd05f24))
* **history:** replayable, redacted, append-only JSONL request history ([a449099](https://github.com/RomanAgaltsev/quiver/commit/a4490992357fd33982ded39e5236ba77e6812271))
* **httpx:** HTTP executor with query, headers, body, auth, timeout, TLS ([e805ec4](https://github.com/RomanAgaltsev/quiver/commit/e805ec4cba286148f611f6fe9d023164341f7e61))
* mvp ([74b5465](https://github.com/RomanAgaltsev/quiver/commit/74b5465cd021eb5ae22942a4e9d46a9758ff3591))
* **render:** pretty/raw/json rendering, verbose headers, redaction, ([1096cc9](https://github.com/RomanAgaltsev/quiver/commit/1096cc9075143611e2fd1f060d109b6b4c621a4e))
* **request:** request model, strict parsing and full validation ([27be209](https://github.com/RomanAgaltsev/quiver/commit/27be2098ecf8ad64adde5e19775b32d36dc3ab71))
* **runner:** orchestrate execution, capture chaining, assertions, ([5166929](https://github.com/RomanAgaltsev/quiver/commit/5166929fc3b1ac6b9352522e28e7b19023d35f91))
* **secret:** redactor for secret values in output and history ([120a1ec](https://github.com/RomanAgaltsev/quiver/commit/120a1ec4e5427f62635754bcb61e6a666ef648e2))


### Bug Fixes

* exit-code contract, gRPC parity, and silent template pass-through ([c7fe282](https://github.com/RomanAgaltsev/quiver/commit/c7fe282393e43cf315669e01eeaa017b163db945))
