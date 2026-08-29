# Roadmap

Post-MVP directions, in rough order of likelihood. Nothing here is promised.

## Phase 1 candidates

- **Load-test slice.** The open decision from the MVP review (review §9):
  a thin fixed-VU load-test mode. Tasks 9 and 3 already shipped the connection
  pooling and `Closer` lifecycle that make this cheap to add; what is missing
  is a reporter and a `--vu`/`--duration` flag pair. Whether it lands in
  Phase 1 or later depends on how the CLI surface settles.
- **Streaming gRPC.** The MVP is deliberately unary-only (spec §3). Server-
  and client-streaming need a rendering story for incremental output first.
- **`env use`.** Explicitly cut from the MVP (spec §6): persisted selection
  state plus a precedence rule against `--env` would make runs depend on
  hidden local state. If enough people ask, the answer is a project-local
  config file with `--env` still winning, not a global.

## Later

- Request chaining beyond captures (fan-out, conditional runs).
- A TUI history browser over the JSONL log.
- Export/import from other clients (Insomnia, Bruno).
