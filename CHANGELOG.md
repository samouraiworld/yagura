# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-11-11

### Added

- Scaleway collector: month-to-date consumption per project, category and currency (`scaleway_consumption`), configured with `YAGURA_SCALEWAY_*`; the `YaguraMonthlyCostOverBudget` alert and a "Costs" dashboard row.

### Fixed

- Statuspage: the `maintenance` indicator is now a known state, exported as `-2`, with no warning and no item error, so `YaguraCollectorPartialFailure` no longer fires during a provider's maintenance. The dashboard maps `-2` to "maintenance".
- Gandi billing: a prepaid account without a warning threshold no longer counts an item error on every poll; the balance is exported and the threshold series omitted. A threshold that is present but not a number now counts an item error instead of failing the whole poll.
- Dashboard: the README now explains that the `datasource` variable lists Prometheus-type data sources only, and how VictoriaMetrics users connect it through a Prometheus-type data source.
- Configuration: an inline secret or a `_FILE` path made only of whitespace (any Unicode space) is now a startup error naming the variable, instead of enabling a collector that fails every poll. A secret file holding only Unicode spaces now counts as empty. This applies to every secret variable (`YAGURA_GANDI_TOKEN`, `YAGURA_SCALEWAY_SECRET_KEY`).

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
