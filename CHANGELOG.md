# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

The MVP's post-implementation review, applied.

### Added

- gRPC requests honour `auth:` profiles (bearer, basic, apikey), and `qv grpc`
  gains `--bearer` / `--user`.
- `{{env:NAME}}` resolves anywhere a template can appear, including inside a
  request file, and the resolved value joins the redaction set.
- Ad-hoc `-H`, `-q` and `--metadata` values are template-expanded.
- Ad-hoc calls are recorded in history.
- A request body with no explicit `Content-Type` gets a sniffed default.
- Collection-level `timeout:` default.
- `--env` accepts `.yml` as well as `.yaml`.
- Pretty output highlights JSON bodies.
- The hermetic example covers all three protocols and is run by CI and by a Go
  test.
- `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, `.goreleaser.yml` and a
  tag-triggered release workflow.

### Changed

- **`qv grpc` takes `<TARGET> <METHOD>`**, matching grpcurl and the spec. The
  reverse order is still accepted, since only one argument can contain a slash.
- Configuration errors raised *during* a run — an unresolved variable, an unknown
  auth profile, an unreadable `body_file`, a capture path that does not resolve —
  exit **2** instead of 1, as the README has always documented.
- A gRPC call that never reached a server is a transport failure (exit 1) rather
  than an inspectable exit-0 response. A status the server chose to return is
  still an inspectable response.
- gRPC status names (`OK`, `NOT_FOUND`) work with every assertion operator, not
  only `eq`. `op: ne, value: "OK"` used to always pass.
- A hand-written URL query string is left byte-for-byte alone when the request
  declares no `query:` block; merged entries are added rather than replacing.
- `--dry-run` prints the URL that will actually be requested, labels gRPC
  metadata as metadata, and creates no files.
- Assertion `value:` is optional but distinguishable, so `value: ""` asserts that
  a field *is* the empty string.
- Folder runs skip dot-directories and YAML that is not a request, so a
  collection can be a repository root.
- The failure summary reports real counts and the first cause instead of
  restating the exit code as a count.
- `qv version` falls back to the module version stamped by `go install`.

### Fixed

- **`LICENSE` was committed as a 0-byte file**, leaving the repository
  all-rights-reserved since commit one. It now carries the Apache-2.0 text.
- `Registry.Close()` panicked on a func-valued executor: `ExecutorFunc` is a func
  type, unhashable, and was being used as a map key.
- An explicit `Authorization` header is no longer silently overwritten by the
  auth profile the request references.
- A present-but-empty header is captured as a value rather than reported missing,
  so `capture` and `assert` agree about the same response.
- `env.Resolve` no longer mutates the caller's `proto_files` slice.
- An `apikey` auth profile with no header name is rejected at load time instead
  of being a silent no-op that surfaces later as a 401.
- History write failures are reported instead of discarded.
- `--collection` pointing at a missing directory or one with no `collection.yaml`
  is an error rather than a silently empty collection.
- The collection-root fallback no longer resolves to the parent of a directory
  target.
