# Contributing to quiver

Thanks for looking. quiver is Apache-2.0 and contributions are welcome.

## Getting set up

```sh
git clone https://github.com/RomanAgaltsev/quiver && cd quiver
task build      # produces ./qv with the version stamped in
task test       # go test -shuffle=on ./...
task lint       # golangci-lint v2
```

`task test:race` is what CI runs. On Windows `-race` needs a C toolchain (MSYS2
or TDM-GCC); if you do not have one, `task test` is the local gate and CI covers
the rest.

If `go test` fails with `fork/exec …: Access is denied`, an antivirus is
blocking execution out of the temp directory. `task test:binary PKG=<name>`
builds that package's test binary into `./bin` and runs it from there.

## What a change should look like

- **Tests come first, and they must be able to fail.** Several defects in this
  repository survived because the only test for the area exercised a different
  code path — an exit-code test that used a parse error rather than a
  resolve-time error, for instance. When you fix a bug, write the test against
  the *specific* path that was wrong.
- **Comments explain decisions, not mechanics.** If a line looks odd, say why the
  obvious version does not work. Several comments here record what a previous
  attempt got wrong; keep that habit.
- **Config errors and run failures are different.** Anything that is the request
  definition's fault is a `core.ConfigError` and exits 2; a transport failure or
  a failed assertion exits 1. CI depends on telling them apart.
- **Never let a bad definition reach the wire silently.** An unresolved template,
  an unusable auth profile, or a message that does not fit its schema must be an
  error *before* anything is sent.
- **Protocols stay symmetric.** If HTTP does something — apply auth, report a
  dead target as an error, default a content type — gRPC and GraphQL must agree,
  or the difference must be documented and deliberate.

## Before opening a pull request

```sh
task fmt && task lint && task test && task vuln
```

CI additionally runs the race detector on Linux and Windows, a coverage floor on
`internal/env` and `internal/render`, and the hermetic example collection end to
end.

## Design docs

Specs and implementation plans live in a separate private vault, so this
repository carries only `ROADMAP.md`. Open an issue to discuss direction before
building something large.
