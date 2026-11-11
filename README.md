# yagura

yagura (Japanese for "watchtower") is a Prometheus exporter that watches the
external cloud and SaaS services a team depends on but does not run: their
public status, the inventory the team owns there (domains, projects, sites),
and what they cost. It polls each provider's API read-only in the background
and exposes the cached values on `/metrics`, so any Prometheus or
VictoriaMetrics setup can scrape it, graph it in Grafana and alert on it.

## Features

- Read-only: only `GET` requests are sent, to provider APIs and, by default,
  to four public status pages.
- Poll-and-cache: scrapes never call a provider; `/metrics` reads memory.
- Self-monitoring metrics for every poller, plus request and build metrics.
- Configuration from environment variables only; secrets also accepted as files.
- Optional TLS and basic auth through the standard exporter-toolkit web config.
- Example Grafana dashboard and Prometheus/vmalert alert rules.

## Quick start

The image is built from this repository (`make docker`). Once published it
will be available as `ghcr.io/samouraiworld/yagura:latest`.

```sh
docker run -p 10100:10100 --env-file .env ghcr.io/samouraiworld/yagura:latest
```

With Docker Compose, use the provided `docker-compose.yml`:

```sh
cp .env.example .env   # then fill in the tokens of the providers you use
docker compose up -d
curl -s http://localhost:10100/metrics | grep '^yagura_'
```

## Configuration

All configuration comes from environment variables. A provider is enabled only
when its credential variable (or its `_FILE` variant) is set.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_LISTEN_ADDRESS` | `:10100` | HTTP listen address |
| `YAGURA_WEB_CONFIG_FILE` | empty | exporter-toolkit web config (TLS, basic auth) |
| `YAGURA_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `YAGURA_LOG_FORMAT` | `logfmt` | `logfmt` or `json` |

Every secret variable `X` also accepts `X_FILE`, naming a file whose trimmed
content is the secret (Docker and Kubernetes secrets). Setting both is a
startup error. A `_FILE` secret is re-read before each poll, so a rotated
token is picked up without a restart. Intervals use Go duration syntax with a
minimum of 30s. Provider variables are listed in each provider section below.

## Metric reference

Self-monitoring series for a poller appear once its first poll has completed.
Go runtime and process metrics are also exposed.

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `yagura_collector_up` | gauge | `collector` | 1 if the last poll succeeded, else 0 |
| `yagura_collector_last_success_timestamp_seconds` | gauge | `collector` | Unix time of the last successful poll |
| `yagura_collector_last_duration_seconds` | gauge | `collector` | Duration of the last poll |
| `yagura_collector_last_item_errors` | gauge | `collector` | Items failed or skipped in the last successful poll |
| `yagura_collector_polls_total` | counter | `collector`, `result` | Poll attempts (`success` or `error`) |
| `yagura_statuspage_indicator` | gauge | `provider` | Page indicator: `none`=0, `minor`=1, `major`=2, `critical`=3, anything else -1 |
| `yagura_statuspage_unresolved_incidents` | gauge | `provider` | Unresolved incidents (omitted if the page does not serve them) |
| `yagura_statuspage_active_maintenances` | gauge | `provider` | Active scheduled maintenances (omitted if the page does not serve them) |
| `yagura_statuspage_updated_timestamp_seconds` | gauge | `provider` | Unix time of the page's `updated_at` |
| `yagura_gandi_domain_expiry_timestamp_seconds` | gauge | `domain` | Unix time at which the domain registration ends |
| `yagura_gandi_domain_autorenew` | gauge | `domain` | 1 if the domain renews automatically, else 0 |
| `yagura_gandi_prepaid_balance` | gauge | `currency` | Prepaid account balance (omitted if the account has no prepaid) |
| `yagura_gandi_prepaid_warning_threshold` | gauge | `currency` | Balance below which Gandi sends a warning (omitted if the account has no prepaid) |
| `yagura_api_requests_total` | counter | `provider`, `code` | Outbound requests (HTTP status, `invalid` for a status outside 100-599, or `error` for network failures) |
| `yagura_build_info` | gauge | `version`, `revision`, `goversion` | Build metadata, always 1 |

## Scrape examples

Prometheus, using `scrape_interval: 60s` (pollers refresh far less often than
that, so a faster scrape adds nothing):

```yaml
scrape_configs:
  - job_name: yagura
    scrape_interval: 60s
    static_configs:
      - targets: ["yagura.example.com:10100"]
```

vmagent uses the same `scrape_configs` YAML.

Grafana Alloy:

```alloy
prometheus.scrape "yagura" {
  targets         = [{"__address__" = "yagura.example.com:10100"}]
  scrape_interval = "60s"
  forward_to      = [prometheus.remote_write.default.receiver]
}

prometheus.remote_write "default" {
  endpoint {
    url = "https://prometheus.example.com/api/v1/write"
  }
}
```

## Providers

### Statuspage

Polls any Atlassian Statuspage-compatible status page and exposes its overall
indicator, the number of unresolved incidents and active maintenances, and
the time the page was last updated. It needs no credentials and is enabled by
default for four public pages (Supabase, Scaleway, Netlify, Gandi). One poller
per page, named `statuspage_<name>`, with the `provider` label set to the
page name. Metrics are listed in the [metric reference](#metric-reference).

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_STATUSPAGE_ENABLED` | `true` | Disable the collector entirely |
| `YAGURA_STATUSPAGE_PAGES` | `supabase=https://status.supabase.com,scaleway=https://status.scaleway.com,netlify=https://www.netlifystatus.com,gandi=https://status.gandi.net` | Comma-separated `name=baseURL`; replaces the default list |
| `YAGURA_STATUSPAGE_EXTRA_PAGES` | empty | Same format, appended to the list |
| `YAGURA_STATUSPAGE_INTERVAL` | `1m` | Poll interval (minimum 30s) |

An unknown or missing indicator is exported as `-1` and counts in
`yagura_collector_last_item_errors`.

Names must match `^[a-z0-9_]+$` and be unique; URLs must be `https`
without query or fragment. Redirects are not followed, so use the final URL of
the page. Some pages only serve `status.json`: when the
incidents or maintenances endpoint is missing, the matching metric is omitted
for that page and the poll still succeeds.

### Gandi

Watches the domains held at Gandi and the prepaid account balance through the
Gandi API v5 (`https://api.gandi.net`). It is enabled when `YAGURA_GANDI_TOKEN`
(or `YAGURA_GANDI_TOKEN_FILE`) is set and runs two pollers:

- `gandi_domains` lists every domain and exports its registration end date and
  whether it renews automatically.
- `gandi_billing` reads the billing information and exports the prepaid balance
  and its warning threshold. An account without prepaid exports no prepaid
  series and the poll still succeeds.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_GANDI_TOKEN` (or `_FILE`) | empty | Personal access token; enables the collector |
| `YAGURA_GANDI_SHARING_ID` | empty | Organization ID (UUID): restricts the domain list to it and reads its billing account |
| `YAGURA_GANDI_DOMAINS_INTERVAL` | `6h` | Domains poll interval (minimum 30s) |
| `YAGURA_GANDI_BILLING_INTERVAL` | `1h` | Billing poll interval (minimum 30s) |

Metrics are listed in the [metric reference](#metric-reference). A domain
without a valid registration end date is skipped, and a domain without an
autorenew value exports no autorenew series; both are logged and count in
`yagura_collector_last_item_errors`.

**Token setup.** In the Gandi account settings, create a personal access token
(PAT):

- restrict it to the one organization yagura should watch, and set
  `YAGURA_GANDI_SHARING_ID` to that organization's ID;
- give it an expiry; one year is a reasonable choice, with the
  `YaguraCollectorFailing` alert to tell you when it has expired;
- grant only the narrowest permission that lets it see domains and the
  narrowest one that lets it see billing information. Both pollers run
  whenever the token is set, so the token needs both.

**Caveat, not confirmed:** we found no published list of Gandi PAT
permissions, so their exact labels are not confirmed, for domains or for
billing. Secondary sources name the domain permission "See and renew domain
names". If that is right, Gandi offers no strictly read-only domain
permission, and a leaked yagura token could renew domains, which spends
money. yagura itself only sends `GET` requests, enforced by a guard on every
request (see the [security model](#security-model)), but that guard protects
against yagura, not against someone who obtains the token. Keep the token in
a file or a secret store, restrict it to one organization and give it an
expiry.

With this collector on, `/metrics` shows every domain name of the
organization and its prepaid balance. Turn on the web config (TLS, basic
auth) or keep the port reachable only by your Prometheus server.

For domains held at other registrars, use a WHOIS-based exporter such as
[`shift/domain_exporter`](https://github.com/shift/domain_exporter). TLS
certificate expiry stays the job of
[blackbox_exporter](https://github.com/prometheus/blackbox_exporter).

## Target scale

yagura is sized for about 200 entities per provider per instance with the
default intervals. Beyond that, raise the poll intervals.

## Dashboard and alerts

- `dashboards/yagura-overview.json`: classic Grafana dashboard JSON. Import it
  and pick your Prometheus data source in the `datasource` variable.
- `alerts/yagura.rules.yml`: example rules for Prometheus and vmalert. The
  `YaguraExporterDown` rule assumes the scrape job is named `yagura`. Check
  the rules with `make rules` (needs Docker).

## Security model

- Read-only: only `GET` requests are sent to provider APIs. A guard enforces
  this on every request. When an HTTPS proxy is configured, the transport
  opens a `CONNECT` tunnel to that proxy.
- Requests go to the provider APIs you configure. By default yagura also
  requests four public status pages (no credentials, https only); disable this
  with `YAGURA_STATUSPAGE_ENABLED=false` or replace the list with
  `YAGURA_STATUSPAGE_PAGES`. Requests go through `HTTPS_PROXY` / `HTTP_PROXY`
  if the operator sets them.
- Tokens are read from the environment or from `_FILE` files, re-read on each
  poll, and never logged.
- `/metrics`, `/healthz` and `/` are all behind the optional exporter-toolkit
  web config (TLS, basic auth). With basic auth on, a probe of `/healthz`
  needs the same credentials.

### Minimum token scopes

Each provider section lists the minimum scopes its token needs.
