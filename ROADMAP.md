# Quiver Roadmap

Post-MVP phases, in order. Each is its own spec → plan → implement cycle.

Design north star: **one open tool that unifies multi-protocol API work — request
client, spec awareness, incumbent import, and load testing — that today requires
gluing three or four separate tools together.**

---

## Phase 1 — Load testing (the signature differentiator)

Promote any saved request or folder into a load test, with no rewrite:

```sh
qv load requests/checkout/ --rate 200 --duration 30s --setup requests/auth/
```

- **Engine: [metronome](https://github.com/RomanAgaltsev/metronome) v0.4.0**, pinned
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

## Phase 2 — Spec-driven generation

Turn an API description into a ready-to-run collection.

- **OpenAPI → collection** via `pb33f/libopenapi`: one request file per operation, with
  params, example bodies, and security → auth-profile stubs.
- **proto → collection** from a `.proto` file or a reflection endpoint, with JSON
  message skeletons derived from the descriptors.
- **GraphQL introspection → collection** from a live endpoint or an SDL file.
- `qv gen openapi spec.yaml -o ./collection/`, `qv gen proto …`, `qv gen graphql …`.

Generated files carry a provenance field so regeneration can diff and merge rather than
clobber hand edits.

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
