# Security policy

## Supported versions

quiver is pre-1.0. Only the latest tagged release receives fixes.

## Reporting a vulnerability

Please report privately rather than opening a public issue: use GitHub's
[Report a vulnerability](https://github.com/RomanAgaltsev/quiver/security/advisories/new)
form. Expect an acknowledgement within a few days.

## What is in scope

`qv` is a client, not a server, but it handles credentials, so the following
count as vulnerabilities rather than ordinary bugs:

- **Secret leakage.** Any value resolved through `{{env:NAME}}` must be redacted
  in rendered output, in `--dry-run`, and in every history record. A path that
  prints or persists one in the clear is in scope. `--show-secrets` is the only
  intended way to see them.
- **History permissions.** `.qv/history/history.jsonl` is written 0600 and is
  gitignored by `qv init`. Widening those permissions, or recording a credential
  the redactor does not cover, is in scope.
- **TLS.** Certificate verification is on by default. `--insecure` is the only
  way to disable it, and no other flag may imply it.
- **Local file access.** `body_file` and `proto_files` resolve relative to the
  request file. A collection that reads outside its own tree without the user
  saying so is in scope.

## What is not

Anything that requires the attacker to already control the collection files or
the process environment: those are the tool's trusted input, by design.
