# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
