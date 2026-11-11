# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-11-11

### Added

- Gandi collector: domain registration end date and autorenew (`gandi_domains`), prepaid balance and warning threshold (`gandi_billing`), configured with `YAGURA_GANDI_*`; the `YaguraDomainExpiringWithoutAutorenew` and `YaguraGandiPrepaidBalanceLow` alerts and a "Domains" dashboard row.

### Fixed

- Errors about `YAGURA_*` log, boolean, duration and secret variables no longer repeat the offending value or the secret file path, so a token set in one of them by mistake cannot reach the logs.

## [0.1.0] - 2026-11-11

### Added

- Configuration from `YAGURA_*` environment variables, with `_FILE` variants for every secret.
- GET-only HTTP client with retries and rate limiting.
- Poller scheduler with self-monitoring metrics.
- `/metrics` and `/healthz` endpoints and a landing page.
- Distroless container image and a Docker Compose example.
- Statuspage collector for Atlassian Statuspage-compatible pages (indicator, incidents, maintenances), the `YaguraProviderIncident` alert and a "Provider status" dashboard row.
- CI and release workflows, secrets scanning and Dependabot.
