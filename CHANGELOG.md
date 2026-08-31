# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
