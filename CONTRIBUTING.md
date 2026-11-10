# Contributing

## Development setup

Requirements: Go (version in `go.mod`), Docker (for `make rules`), golangci-lint v2.

```sh
make test lint rules
```

## Tests first

Write the failing test before the code (TDD). Tests must never reach the
network: provider tests use `httptest` servers, and every test package that
builds an `httpx.Client` calls `httpx.SetDialGuard(httpx.LoopbackOnly)` in
`TestMain`.

## Fixture sanitisation

No real organisation names, hostnames, IPs, account IDs, project refs or domains anywhere. Fixtures: RFC 2606 domains (`example.com/.org/.net`), UUIDs `00000000-0000-0000-0000-00000000000N`, Supabase refs `aaaaaaaaaaaaaaaaaaaa`/`bbbbbbbbbbbbbbbbbbbb`, names `demo-…`, invented amounts. Hand-written, never pasted from real responses.

Fixtures live in `internal/<provider>/testdata/`.

## Private denylist

To block organisation-specific names from ever being committed, without
listing them in this repository, keep a gitleaks config file outside the repo
and point `YAGURA_PRIVATE_DENYLIST` at it. The pre-commit hook then runs an
extra gitleaks pass with that config. Example rule:

```toml
[[rules]]
id = "example-corp"
description = "Organisation-specific name"
regex = '''(?i)example-corp'''
```

Install the hooks with `pre-commit install`.

## Releases

A `v<version>` tag publishes only after CI passes on the tagged commit and that
commit is on `main`. Maintainers should protect `v*` tags in the repository
settings (a tag ruleset) so that only they can create them.

## Adding a provider

- [ ] Package `internal/<provider>` implementing the poller contract
- [ ] Sanitised fixtures in `internal/<provider>/testdata/`
- [ ] Configuration variables (secrets with `_FILE` support) and `.env.example`
- [ ] Wiring in `cmd/yagura`
- [ ] README section: token setup with minimum scopes, metrics
- [ ] Alert rules in `alerts/` with promtool tests
- [ ] Dashboard panels
- [ ] CHANGELOG entry
