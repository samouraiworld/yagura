# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Monthly budgets stored by yagura in `YAGURA_STATE_DIR/budgets.json`, with
  optional initial values (`YAGURA_BUDGET_INITIAL_TOTAL`,
  `YAGURA_BUDGET_INITIAL_SCALEWAY`) applied once, and the metric
  `yagura_budget_amount{scope, project, currency}`.
- `yagura_scaleway_project_info{project_id, project_name, project, mapping}`:
  the business project of each Scaleway project seen in billing, by an
  automatic name rule or a manual mapping.
- Optional budget write API on its own listener (`YAGURA_API_LISTEN_ADDRESS`),
  protected by a bearer token (`YAGURA_API_TOKEN` or `YAGURA_API_TOKEN_FILE`):
  list and change budgets and manual project mappings. Off by default.

## [0.5.1] - 2026-11-16

### Fixed

- Supabase: the `supabase_projects` poller no longer sends `timeout_ms` with the health query (`/v1/projects/{ref}/health`). The Management API rejects that parameter with HTTP 400, so every `supabase_projects` poll failed: no project status or service health series, `yagura_collector_up` stayed at 0 and `YaguraCollectorFailing` fired.
- Netlify: a production build that Netlify cancels because nothing changed (state `error`, error message "Canceled build due to no content change") no longer counts as a failed deploy. The `netlify_deploys` poller fetches the 10 latest production deploys of each site and reports the first one that is not such a skipped build, so a real failure followed by skipped builds still shows as failed. Before, `yagura_netlify_last_deploy_failed` was 1 and `YaguraNetlifyLastDeployFailed` fired for every site whose latest production build was skipped. When all 10 are skipped builds, the failed series is 0 and the state and creation time series are omitted.

## [0.5.0] - 2026-11-16

### Added

- Gandi renewal prices: the `gandi_prices` poller exports the one-year renewal price of every domain, excluding taxes (`yagura_gandi_domain_renewal_price`, the catalog price of the domain's TLD at the organization's price grid), polled once a day (a domain listing plus one request per 50 distinct TLDs); configured with `YAGURA_GANDI_PRICES_ENABLED` and `YAGURA_GANDI_PRICES_INTERVAL`. It is on by default with a Gandi token: set `YAGURA_GANDI_PRICES_ENABLED=false` if the token may not read prices (the poller then fails with HTTP 403) or to keep prices off `/metrics`.
- "Costs" dashboard row: monthly bars over the last year for the total of all services (Scaleway measured plus one twelfth of the Gandi renewal estimate for autorenew domains) and for Scaleway alone, against an example budget; the Gandi renewal estimate; Gandi renewals over the next 12 months by month; a note that Netlify and Supabase expose no costs. The "Domain expiry" table shows each domain's renewal price.
- `YaguraTotalMonthlyCostOverBudget` alert: Scaleway month to date plus one twelfth of the Gandi renewal estimate, per yagura instance and currency, over an example budget.

### Fixed

- Netlify: the `netlify_builds` poller decodes the build status of a team (`/api/v1/{account_id}/builds/status`) as the single JSON object Netlify returns, not as an array. Every `netlify_builds` poll failed before, so `yagura_collector_up` stayed at 0 and `YaguraCollectorFailing` fired. A body that is not one JSON object counts as an item error.
- Dashboard: the "Scaleway month to date by project" chart gives eight common categories a fixed colour of their own (other categories are grey) and no longer clips long project names; the "Domain expiry" table lists its columns by name, so extra scrape labels no longer show up.
- README: the Scaleway amount is confirmed to exclude VAT.

## [0.4.0] - 2026-11-12

### Added

- Supabase collector: project status, service health (`supabase_projects`) and disk utilisation (`supabase_disk`) of every project the token can see, configured with `YAGURA_SUPABASE_*`; the `YaguraSupabaseProjectUnhealthy` and `YaguraSupabaseDiskAlmostFull` alerts and a "Supabase" dashboard row. The README also shows how to scrape the per-project Supabase Metrics API directly.
- Netlify collector: state of the last production deploy of every site (`netlify_deploys`) and build minutes used and included per team (`netlify_builds`), configured with `YAGURA_NETLIFY_*`; the `YaguraNetlifyLastDeployFailed` alert and a "Netlify" dashboard row.

### Fixed

- Supabase: the warning logged when the health or disk call of one project fails now keeps the cause (HTTP status or decoding problem) instead of cutting the error text at 64 bytes.

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
