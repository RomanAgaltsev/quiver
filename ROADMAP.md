# Quiver Roadmap

Post-MVP phases, in order. Each is its own spec → plan → implement cycle.

Design north star: **one open tool that unifies multi-protocol API work — request
client, spec awareness, incumbent import, and load testing — that today requires
gluing three or four separate tools together.**

---

## Phase 1 — Load testing (the signature differentiator) — SHIPPED

Shipped in v1.1.0/v1.1.1, and completed in v1.2.0 by adopting metronome v0.9.0:
live percentiles in `--progress`, a per-request breakdown of a folder target, and
`--warmup`. Those three were deferred at v1.1.0 for a reason that no longer holds --
every figure metronome exposed mid-run was cumulative, so a live percentile would
have been a lifetime figure presented as a current one.

Promote any saved request or folder into a load test, with no rewrite:

```sh
qv load requests/checkout/ --rate 200 --duration 30s --setup requests/auth/
```

- **Engine: [metronome](https://github.com/RomanAgaltsev/metronome) v0.9.0**, pinned
  exactly. It is a protocol-agnostic Go load kernel that is already built and released;
  quiver supplies a small `core.Executor` → `metronome.Runner` adapter and nothing else
  about generation, pacing, or statistics.
- **Open-loop pacing by default** (`--rate`, not a fixed pool of virtual users), with
  coordinated-omission-corrected percentiles reported alongside the raw ones.
- `--setup` runs an auth chain once through the Phase 0 sequential runner, so a load
  test inherits captures without re-authoring anything.
- Declarative `thresholds:` → CI exit codes, with a **distinct exit 3 for "the
  measurement is not trustworthy"**, so a generator-bound run is never read as a result
  about the target.

Why first: it is the only capability on this roadmap no competitor has, its engine is
already built, and the Phase 0 seams (`Executor`, the normalized `Response`, gRPC
connection pooling, `Closer`) were built specifically to make it additive.

## Phase 2 — Spec-driven generation — 2a and 2b DELIVERED, 2c to come

Turn an API description into a ready-to-run collection.

- **OpenAPI → collection** via `pb33f/libopenapi` — **SHIPPED in v1.3.0**:
  `qv gen openapi spec.yaml -o ./collection/`. One request file per operation, path
  params as `{{var}}` templates, required/exampled query and header params, JSON body
  from the declared example or a required-only schema skeleton, a status assertion from
  the lowest declared 2xx, and `servers` + `securitySchemes` → collection `defaults` and
  auth profiles whose every credential is an `{{env:...}}` reference.
- **proto → collection** from `.proto` files or a reflection endpoint — **SHIPPED in
  v1.4.0**: `qv gen proto api/*.proto --target host:port -o ./collection/`, or
  `qv gen proto --reflect host:port`. One request file per **unary** RPC grouped by
  service, protojson message skeletons derived from the descriptors, and a
  `status eq OK` assertion. Streaming RPCs are skipped and named in the report.
  **No new dependency** — both descriptor sources were already in `go.mod`, and both
  funnel through one `grpcx.MethodInfo` seam so the generator never branches on
  provenance.
- **GraphQL introspection → collection** from a live endpoint or an SDL file.
  *(2c, not started.)*

Regeneration is governed by a committed **`.qv/gen.lock`** rather than a per-file
provenance field: it records what `qv gen` wrote and what each file looked like, so a
file you have since edited is skipped and reported instead of clobbered, and an
operation that leaves the spec is reported as orphaned and left on disk. There is
deliberately no three-way merge — the reasoning is in the design spec, and the contract
is stated in the README as a promise. 2b reuses that lockfile, writer and report
unchanged — a collection may hold files from both generators and behaves the same
either way — and 2c will too. The one difference: a `--reflect` source records no
`source_hash`, because a live server has no stable bytes to fingerprint, so drift
detection is weaker there and the README says so.

Not covered by 2a, and each named rather than assumed: Swagger 2.0 (refused by name),
response-schema assertions, `--tag`/`--path` filters, and any request body that is not
`application/json`. Not covered by 2b: streaming RPCs (Phase 7), and response-message
assertions beyond the status.

## Phase 3 — Spec linting

Validate API specs from the same tool, built on `daveshanley/vacuum` (Spectral-ruleset
compatible): `qv lint openapi spec.yaml`, custom rulesets, console and JSON/HTML
reports, CI exit codes. Pairs with Phase 2 — generate *and* validate in one tool.

## Phase 4 — Import (Postman / Bruno) + JS-script compatibility

Migrate off the incumbents without losing scripts.

- Postman v2.1 collection JSON and Bruno `.bru` folders → quiver YAML.
- A **`goja`** sandbox (pure-Go ES, no Node, no CGO) implementing the `pm.*` and `bru.*`
  surface real collections actually use. This is the part competitors botch on import.

## Phase 5 — Native scripting

Expose the Phase 4 sandbox as first-class `pre`/`post` blocks with a documented
quiver-native JS API. Captures and assertions stay the declarative fast path; scripting
is the escape hatch. Sequenced after Phase 4 so the engine is built once.

## Phase 6 — TUI

An optional Bubble Tea interactive mode: browse the collection tree, edit, send, inspect,
switch environments. Strictly additive — the CLI stays the primary, scriptable interface.

## Phase 7 — Protocol & auth depth

- **gRPC streaming** (server/client/bidi). Note this is the one planned feature that
  *breaks* the Phase 0 seam rather than extending it: `Response` is a request/response
  contract, not a universal one, and streaming needs its own model.
- **OAuth2** flows (client credentials, auth code + PKCE, device) with token caching.
- WebSocket / SSE, HTTP/3, mTLS and client certificates.

---

## Explicitly cut

- **`qv env use`.** Persisted environment selection would make a run depend on hidden
  local state, and being stateless is what keeps `qv` reproducible in CI. `--env` plus a
  shell alias covers the need. If enough people ask, the answer is a project-local config
  file with `--env` still winning — never a global.

Cross-cutting throughout: keep the CLI scriptable and CI-friendly, keep collections
git-diffable, never require an account or a cloud service, ship a single static binary.
