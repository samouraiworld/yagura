<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/yagura-logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/yagura-logo.svg">
    <img src="assets/yagura-logo.svg" alt="yagura logo" width="140">
  </picture>
</p>

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
- Optional monthly budgets per scope and business project, exposed as metrics
  and editable through a small authenticated write API on its own listener.
- Scaleway monthly cost history: consumption per project and category and
  invoiced totals for the last closed months, kept across restarts.
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
| `YAGURA_STATE_DIR` | empty | Existing directory holding `budgets.json` and `scaleway-history.json`; enables budgets (see [Budgets and write API](#budgets-and-write-api)) and keeps the [Scaleway monthly history](#monthly-history) across restarts |
| `YAGURA_BUDGET_INITIAL_TOTAL` | empty | Initial `total` budget in EUR, applied once when `budgets.json` does not exist |
| `YAGURA_BUDGET_INITIAL_SCALEWAY` | empty | Initial `scaleway` budget in EUR, applied once when `budgets.json` does not exist |
| `YAGURA_API_LISTEN_ADDRESS` | empty | Listen address of the budget write API; empty keeps it off |
| `YAGURA_API_TOKEN` | empty | Bearer token of the write API (secret, at least 32 characters) |

Every secret variable `X` also accepts `X_FILE`, naming a file whose trimmed
content is the secret (Docker and Kubernetes secrets). Setting both is a
startup error, and so is an inline value or a `_FILE` path made only of
whitespace. A `_FILE` secret is re-read before each poll, so a rotated token is
picked up without a restart; a file that is empty after trimming fails that
poll. Intervals use Go duration syntax with a
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
| `yagura_statuspage_indicator` | gauge | `provider` | Page indicator: `none`=0, `minor`=1, `major`=2, `critical`=3, `maintenance`=-2, anything else -1 |
| `yagura_statuspage_unresolved_incidents` | gauge | `provider` | Unresolved incidents (omitted if the page does not serve them) |
| `yagura_statuspage_active_maintenances` | gauge | `provider` | Active scheduled maintenances (omitted if the page does not serve them) |
| `yagura_statuspage_updated_timestamp_seconds` | gauge | `provider` | Unix time of the page's `updated_at` |
| `yagura_gandi_domain_expiry_timestamp_seconds` | gauge | `domain` | Unix time at which the domain registration ends |
| `yagura_gandi_domain_autorenew` | gauge | `domain` | 1 if the domain renews automatically, else 0 |
| `yagura_gandi_domain_renewal_price` | gauge | `domain`, `currency` | Gandi catalog price of a one-year renewal for the domain's TLD, excluding taxes, at the organization's price grid (omitted with an item error when Gandi gives no such price for its TLD) |
| `yagura_gandi_prepaid_balance` | gauge | `currency` | Prepaid account balance (omitted if the account has no prepaid; omitted with an item error if missing) |
| `yagura_gandi_prepaid_warning_threshold` | gauge | `currency` | Balance below which Gandi sends a warning (omitted if the account has no prepaid or no threshold set; omitted with an item error if it is not a number) |
| `yagura_scaleway_consumption_amount` | gauge | `project_id`, `project_name`, `category`, `currency` | Consumption month to date, as reported by the Scaleway billing API; resets on the 1st |
| `yagura_scaleway_consumption_updated_timestamp_seconds` | gauge | none | Unix time of the billing API's last consumption update (omitted if the API does not send a valid one) |
| `yagura_scaleway_project_info` | gauge | `project_id`, `project_name`, `project`, `mapping` | Business project of each Scaleway project seen in the current month's billing data, always 1; `mapping` is `auto` or `manual` (see [Budgets](#budgets-and-write-api)) |
| `yagura_scaleway_monthly_consumption_amount` | gauge | `billing_period`, `project_id`, `project_name`, `project`, `category`, `currency` | Consumption of a billing period (`YYYY-MM`, UTC), excluding VAT and before discounts, for the current month and the last closed months (see [Monthly history](#monthly-history)) |
| `yagura_scaleway_invoice_amount` | gauge | `billing_period`, `kind`, `currency` | Sum of a closed billing period's invoices: `kind="untaxed"` is `total_untaxed` (after discount), `kind="discount"` is `total_discount` (negative) |
| `yagura_scaleway_monthly_last_success_timestamp_seconds` | gauge | `billing_period` | Unix time of the last successful read of a billing period's consumption |
| `yagura_budget_amount` | gauge | `scope`, `project`, `currency` | Monthly budget excluding VAT stored by yagura; `scope` is `total`, `scaleway` or `project` (`project` is empty unless `scope="project"`); an unset budget has no series |
| `yagura_supabase_project_info` | gauge | `project_ref`, `project_name`, `organization_slug`, `region` | Always 1; carries the project's name, organization and region |
| `yagura_supabase_project_status` | gauge | `project_ref`, `status` | State set: 1 for the project's current status, 0 for every other known status; an unknown status gets its own series with 1 |
| `yagura_supabase_service_healthy` | gauge | `project_ref`, `service` | 1 if the service reports `ACTIVE_HEALTHY`, else 0; active projects only; 0 for every requested service when the project's health call fails |
| `yagura_supabase_disk_size_bytes` | gauge | `project_ref` | Size of the project's disk; active projects only |
| `yagura_supabase_disk_used_bytes` | gauge | `project_ref` | Used space on the project's disk; active projects only |
| `yagura_supabase_disk_available_bytes` | gauge | `project_ref` | Available space on the project's disk; active projects only |
| `yagura_netlify_last_deploy_info` | gauge | `site`, `account`, `state` | Always 1; carries the raw state of the site's last production deploy, ignoring builds skipped for no content change (`ready`, `error`, `building`, …), and its team slug; omitted for a site with no production deploy or whose 10 latest production deploys are all skipped builds (omitted with an item error for a deploy without a state) |
| `yagura_netlify_last_deploy_failed` | gauge | `site` | 1 if the site's last production deploy, ignoring builds skipped for no content change, is in the `error` state, else 0 (0 when the 10 latest production deploys are all skipped builds); omitted for a site with no production deploy (omitted with an item error for a deploy without a state) |
| `yagura_netlify_last_deploy_created_timestamp_seconds` | gauge | `site` | Unix time at which the site's last production deploy, ignoring builds skipped for no content change, was created; omitted for a site with no production deploy or whose 10 latest production deploys are all skipped builds (omitted with an item error if the time is missing or invalid) |
| `yagura_netlify_build_minutes_used` | gauge | `account` | Build minutes the team has used in the current billing period (omitted with an item error if the API sends none) |
| `yagura_netlify_build_minutes_included` | gauge | `account` | Build minutes included in the team's plan (omitted if the API sends no usable number) |
| `yagura_netlify_build_period_end_timestamp_seconds` | gauge | `account` | Unix time at which the team's current build billing period ends (omitted with an item error if missing or invalid) |
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

A `maintenance` indicator is exported as `-2`: it is a normal page state, so
it logs no warning, counts no item error and stays below the
`YaguraProviderIncident` threshold (`>= 2`). An unknown or missing indicator
is exported as `-1` and counts in `yagura_collector_last_item_errors`.

Names must match `^[a-z0-9_]+$` and be unique; URLs must be `https`
without query or fragment. Redirects are not followed, so use the final URL of
the page. Some pages only serve `status.json`: when the
incidents or maintenances endpoint is missing, the matching metric is omitted
for that page and the poll still succeeds.

### Gandi

Watches the domains held at Gandi, their renewal prices and the prepaid
account balance through the Gandi API v5 (`https://api.gandi.net`). It is
enabled when `YAGURA_GANDI_TOKEN` (or `YAGURA_GANDI_TOKEN_FILE`) is set and runs
two pollers, plus a third one unless it is turned off:

- `gandi_domains` lists every domain and exports its registration end date and
  whether it renews automatically.
- `gandi_billing` reads the billing information and exports the prepaid balance
  and its warning threshold. An account without prepaid exports no prepaid
  series and the poll still succeeds. A prepaid account without a warning
  threshold exports the balance only, also without error; the
  `YaguraGandiPrepaidBalanceLow` alert compares the two series, so it cannot
  fire until a threshold is set in Gandi.
- `gandi_prices` lists the domains again, asks Gandi's catalog price of a
  one-year renewal for every distinct TLD
  (`GET /v5/billing/price/domain?name=.<tld>&processes=renew`, one request per
  50 TLDs) and exports that price, excluding taxes, for each domain of the TLD.
  The price is the one Gandi quotes for your organization's price grid;
  promotions and premium domains may make the actual renewal differ. A domain
  whose TLD Gandi renews for two years at least gets no price series and is
  left out of the estimate (logged as a warning). A domain whose TLD gets no
  usable renewal price at all is skipped as an item error, and a poll where
  every domain is skipped that way fails. Prices change rarely, so it polls
  once a day.
  Set `YAGURA_GANDI_PRICES_ENABLED=false` to turn it off, for example if your
  token is not allowed to read prices (the poll then fails with HTTP 403 and
  `YaguraCollectorFailing` fires).

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_GANDI_TOKEN` (or `_FILE`) | empty | Personal access token; enables the collector |
| `YAGURA_GANDI_SHARING_ID` | empty | Organization ID (UUID): restricts the domain list to it and reads its billing account |
| `YAGURA_GANDI_DOMAINS_INTERVAL` | `6h` | Domains poll interval (minimum 30s) |
| `YAGURA_GANDI_BILLING_INTERVAL` | `1h` | Billing poll interval (minimum 30s) |
| `YAGURA_GANDI_PRICES_ENABLED` | `true` | `false` turns the `gandi_prices` poller off |
| `YAGURA_GANDI_PRICES_INTERVAL` | `24h` | Renewal price poll interval (minimum 30s) |

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
  narrowest one that lets it see billing information. The domain and billing
  pollers always run when the token is set, so the token needs both. Which
  permission the price poller needs is not confirmed (see the caveat below);
  that poller can be turned off.

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

With this collector on, `/metrics` shows every domain name the token can see
(only the organization's when `YAGURA_GANDI_SHARING_ID` is set), their renewal
prices and the prepaid balance. Turn on the web config (TLS, basic auth) or
keep the port reachable only by your Prometheus server.

For domains held at other registrars, use a WHOIS-based exporter such as
[`shift/domain_exporter`](https://github.com/shift/domain_exporter). TLS
certificate expiry stays the job of
[blackbox_exporter](https://github.com/prometheus/blackbox_exporter).

### Scaleway

Reads the organization's consumption for the current month through the
Scaleway billing API (`https://api.scaleway.com`). It is enabled when
`YAGURA_SCALEWAY_SECRET_KEY` (or `YAGURA_SCALEWAY_SECRET_KEY_FILE`) is set and
runs the poller `scaleway_consumption`, plus `scaleway_history` for the
[monthly history](#monthly-history). The API returns one row per product,
resource or SKU; yagura sums them per project, category and currency, so
products, resources and SKUs never become labels.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_SCALEWAY_SECRET_KEY` (or `_FILE`) | empty | Secret key of an IAM application; enables the collector |
| `YAGURA_SCALEWAY_ORGANIZATION_ID` | empty | Organization ID (UUID) whose consumption is read; required when the collector is enabled |
| `YAGURA_SCALEWAY_INTERVAL` | `1h` | Poll interval (minimum 30s) |
| `YAGURA_SCALEWAY_HISTORY_MONTHS` | `24` | Closed months kept in the monthly history, 0 to 60; `0` turns the history and its three metrics off |
| `YAGURA_SCALEWAY_HISTORY_INTERVAL` | `24h` | How often each closed month and the invoice list are read again (minimum 1h) |

Metrics are listed in the [metric reference](#metric-reference). The amount
is month to date and resets on the 1st, at the first successful poll of the
month. It usually grows during the month, but a credit or correction from
Scaleway can lower it, even below zero. A project with no consumption yet this
month has no series.
It is the value the billing API reports, which can lag behind real usage;
`yagura_scaleway_consumption_updated_timestamp_seconds` shows when Scaleway
last updated it. A row without an amount or currency is skipped, logged and
counted in `yagura_collector_last_item_errors`. A row without a project name
keeps an empty `project_name` label, so the total is never undercounted. The
current API reference lists no project name on consumption rows, so expect
`project_name` to be empty; the dashboard then shows project IDs.

**VAT:** the amount excludes VAT. Checked on a real account: the month total
yagura exports matches the consumption the Scaleway CLI reports, and the
separate taxes endpoint adds VAT on top of it.

**Key setup.** In the Scaleway console, under IAM:

- create an IAM *application* rather than using a user's API key, so the key
  is not tied to a person;
- attach a policy to the application with a single rule: the
  `BillingReadOnly` permission set, scoped to the organization ("List and read
  access to billing information"). No project permission is needed: the
  consumption rows already carry project IDs and names. The same permission
  set covers the invoice list the monthly history reads;
- generate an API key for the application, with an expiry date; the
  `YaguraCollectorFailing` alert tells you when it has expired;
- set `YAGURA_SCALEWAY_SECRET_KEY` to the key's *secret* key (not its access
  key) and `YAGURA_SCALEWAY_ORGANIZATION_ID` to the organization ID shown in
  the console's organization settings. Both are UUIDs: do not swap them, or
  the secret key is sent to Scaleway as a query parameter.

Not confirmed either: whether `BillingReadOnly` alone is enough to filter by
organization. If the poller fails and `yagura_api_requests_total` shows code
`403` for `scaleway`, check the policy and its scope.

With this collector on, `/metrics` shows the organization's project names and
IDs and what each one has cost this month and, with the monthly history, in
each past month of the window, with the invoiced totals and discounts. Turn on the web config (TLS, basic
auth) or keep the port reachable only by your Prometheus server.

The `YaguraMonthlyCostOverBudget` alert fires when the month-to-date total of
a currency, for one yagura instance, stays above 100 for an hour; replicas
watching the same organization each raise it. 100 is an example: edit it in `alerts/yagura.rules.yml` to your budget. While polls
fail, yagura keeps serving the last amount it read, so after the 1st the alert
resolves only once a poll succeeds; `YaguraCollectorFailing` covers that case.

#### Monthly history

`yagura_scaleway_monthly_consumption_amount` gives the consumption of each
billing period: the current month and the last `YAGURA_SCALEWAY_HISTORY_MONTHS`
closed months. A billing period is a calendar month in UTC, as Scaleway
defines it. Rows are summed per project ID, category and currency, on the same
base as `yagura_scaleway_consumption_amount` (excluding VAT, before
discounts). The month is in the `billing_period` label, never in the sample
time, and the business project is in the `project` label, so no join is
needed and past projects stay visible:

```promql
sum by (billing_period, category) (yagura_scaleway_monthly_consumption_amount{project="acme"})
```

- **Current month:** built from the `scaleway_consumption` poll, with no extra
  API call, so it has the same values as `yagura_scaleway_consumption_amount`.
  At the month change its series keep their labels and become a closed month,
  which is read again at once (within 15 minutes).
- **Closed months:** the `scaleway_history` poller reads each one with
  `billing_period=YYYY-MM`, newest first, then lists the invoices. It checks
  every 15 minutes what is due; a month is read again once its last success is
  older than `YAGURA_SCALEWAY_HISTORY_INTERVAL`, or when it was last read
  before it ended. At most `YAGURA_SCALEWAY_HISTORY_MONTHS` + 1 calls per
  interval, plus extra pages: 25 a day by default.
- **Invoices:** `yagura_scaleway_invoice_amount` sums `total_untaxed`
  (`kind="untaxed"`) and `total_discount` (`kind="discount"`, negative) over
  every invoice whose `billing_period` falls in a closed month of the window.
  Discounts exist only per invoice, never per project. A month without an
  invoice, such as the current month, has no invoice series.
- **Names:** a project gets one `project_name` for every month: its name in
  the most recent month where it has one, ignoring a name equal to the
  project ID. The billing rows carry no rename date, so when a project has
  two names within that month, the alphabetically smallest is used. The
  billing API names a deleted project by its ID; a project never seen with
  another name keeps its ID as `project_name` and `project`. `project` follows
  the [business project rule](#business-projects) at scrape time, so a mapping
  set through the API applies to every month at the next scrape.
- **Errors:** an empty list (a month before the first bill) is a success
  with no amount series. A closed month's read fails when a row has no
  amount or currency, when the data changes between pages, or when a page is
  empty before the announced total. Just after the month change, an answer
  last updated before the month began is not taken as the new month. A failed read keeps the stored values (never 0), logs
  one error line and is retried an hour later; until it succeeds the
  `scaleway_history` poll counts as failed in `yagura_collector_up`. A month
  never read with success has no series.
  `yagura_scaleway_monthly_last_success_timestamp_seconds` shows when each
  month was last read, empty months included.
- **Storage:** with `YAGURA_STATE_DIR` set, the months, the invoice totals
  and the last success times are saved to `scaleway-history.json` (mode 0600,
  replaced atomically, at most 32 MiB: a larger history is kept in memory and
  not saved, with an error). At startup the stored months are exposed at once
  and only due months are read. An unreadable or corrupt file does not stop
  yagura: it is renamed `scaleway-history.json.invalid-<UTC time>` and kept
  for inspection, an error is logged, and every month is read again from the
  billing API (delete the renamed file once checked). Without
  `YAGURA_STATE_DIR`, or when the directory is not writable (a warning is
  logged), the history is kept in memory and read again in full after each
  restart.
- **Late corrections:** a month read just after it ended is read again one
  interval later, so a correction Scaleway makes in the first days of the
  next month shows within `YAGURA_SCALEWAY_HISTORY_INTERVAL`.

Series: one per month, project, category and currency (about 25 per month on
an organization with 20 projects, so about 650 for 25 months), 2 invoice series
per invoiced month and 1 success series per month read.

#### Bucket size and object count (Cockpit)

yagura does not read Object Storage bucket metrics: Scaleway already publishes
them in Cockpit, its managed metrics service, as
`object_storage_bucket_usage_bytes_total` (bytes stored) and
`object_storage_bucket_objects_total` (object count), refreshed hourly. Both
ways below need a Cockpit token that can only query metrics (no push, logs,
traces or alerting permissions), created in the same region as the metrics
data source, and the data source URL shown in the
Cockpit console, `https://<data-source-id>.metrics.cockpit.<region>.scw.cloud`.

**Grafana data source** (the data stays in Cockpit): add a Prometheus data
source whose URL is the data source URL followed by `/prometheus`, with a
custom HTTP header `X-Token` set to the token. As a provisioning file, where
Grafana reads `SCW_COCKPIT_TOKEN` from its environment so the token stays out
of the file:

```yaml
apiVersion: 1
datasources:
  - name: Scaleway Cockpit
    type: prometheus
    access: proxy
    url: https://<data-source-id>.metrics.cockpit.<region>.scw.cloud/prometheus
    jsonData:
      httpHeaderName1: X-Token
    secureJsonData:
      httpHeaderValue1: ${SCW_COCKPIT_TOKEN}
```

**Federation** (copies the series into your own Prometheus): scrape the
Cockpit `/federate` endpoint. It is in beta: Scaleway does not bill it during
the beta and says it may be billed afterwards. Keep `match[]` narrow, since
every federated sample may count. Not confirmed: how far back `/federate`
looks for the latest sample. These metrics are written once an hour, so if it
only returns recent samples, the federated series will have gaps; the Grafana
data source above does not have that problem. Prometheus 2.55 or later (for
`http_headers`):

```yaml
scrape_configs:
  - job_name: scaleway-cockpit
    scrape_interval: 3m
    honor_labels: true
    follow_redirects: false
    scheme: https
    metrics_path: /federate
    params:
      'match[]': ['{__name__=~"object_storage_bucket_(usage_bytes|objects)_total"}']
    http_headers:
      X-Token:
        files: [/etc/prometheus/scw-cockpit-token]
    static_configs:
      - targets: ["<data-source-id>.metrics.cockpit.<region>.scw.cloud"]
```

Grafana Alloy 1.8 or later:

```alloy
local.file "scw_cockpit_token" {
  filename  = "/etc/alloy/scw-cockpit-token"
  is_secret = true
}

prometheus.scrape "scaleway_cockpit" {
  targets          = [{"__address__" = "<data-source-id>.metrics.cockpit.<region>.scw.cloud"}]
  scheme           = "https"
  metrics_path     = "/federate"
  honor_labels     = true
  follow_redirects = false
  scrape_interval  = "3m"
  params           = {"match[]" = ["{__name__=~\"object_storage_bucket_(usage_bytes|objects)_total\"}"]}
  http_headers     = {"X-Token" = [local.file.scw_cockpit_token.content]}
  forward_to       = [prometheus.remote_write.default.receiver]
}
```

Redirects are turned off so the token is never sent to another host. Write
the token file without a trailing newline.

For per-resource consumption and an inventory of instances, volumes and other
resources, use [`promhippie/scw_exporter`](https://github.com/promhippie/scw_exporter).
Its inventory collectors need read access to those resources, beyond
`BillingReadOnly`: give it its own IAM application and key rather than
widening yagura's policy.

### Supabase

Watches the status, service health and disk usage of Supabase projects
through the Supabase Management API (`https://api.supabase.com`). It is
enabled when `YAGURA_SUPABASE_TOKEN` (or `YAGURA_SUPABASE_TOKEN_FILE`) is set
and runs two pollers, which share one rate limiter:

- `supabase_projects` lists the projects and exports their status, then asks
  each active project (status `ACTIVE_HEALTHY` or `ACTIVE_UNHEALTHY`) for the
  health of the services in `YAGURA_SUPABASE_HEALTH_SERVICES`. A paused
  project has the status `INACTIVE`: it is listed, but its health is not
  requested and it exports no health series.
- `supabase_disk` lists the projects again and reads the disk utilisation of
  each active project.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_SUPABASE_TOKEN` (or `_FILE`) | empty | Personal access token (`sbp_…`); enables the collector |
| `YAGURA_SUPABASE_HEALTH_INTERVAL` | `1m` | Projects and health poll interval (minimum 30s) |
| `YAGURA_SUPABASE_HEALTH_SERVICES` | `auth,db,pooler,realtime,rest,storage` | Services whose health is requested, comma-separated; allowed: `auth`, `db`, `db_postgres_user`, `pooler`, `realtime`, `rest`, `storage`, `pg_bouncer` |
| `YAGURA_SUPABASE_DISK_INTERVAL` | `5m` | Disk poll interval (minimum 30s) |

Metrics are listed in the [metric reference](#metric-reference). When the
health call of one project fails while other projects answer, yagura logs it,
counts it in `yagura_collector_last_item_errors` and reports every requested
service of that project as unhealthy; a failed disk call omits that project's
disk series. When the call of every active project fails (for example with a
single project), the poll fails instead: yagura keeps serving the last values
and `YaguraCollectorFailing` fires after 15 minutes. A project that disappears
between the list and the call (HTTP 404) is left out without an error. A 401
or 403 fails the whole poll, so a token that lacks one permission makes its
poller fail; `yagura_api_requests_total` then shows code `403` for `supabase`.

Each poll must finish within 80% of its interval, and the health calls run
four at a time. With more than a few dozen active projects, raise
`YAGURA_SUPABASE_HEALTH_INTERVAL`.

**Token setup.** On the Access Tokens page of your Supabase account settings,
create a *scoped* personal access token:

- start from the **No access** preset, not **Read-only**: the Read-only
  preset also lets the token read API keys and secrets;
- restrict it to the organization yagura should watch. **Not confirmed:**
  whether a token restricted to some projects lists only those. If it lists
  others, their health calls answer 403 and every poll fails; restrict the
  token to the whole organization then;
- give it an expiry date (Supabase allows up to one year); the
  `YaguraCollectorFailing` alert tells you when it has expired;
- grant only these **Read** permissions:
  - "Projects (account-wide)", to list the projects;
  - "Project Settings", for service health. **Not confirmed:** this label is
    taken from the Supabase permission table for the health endpoint, whose
    internal name is `project_admin_read`; it has not been checked with a
    real token yet. Nor has whether it also lets the token read the
    project's API keys; see the check below;
  - "Disk Config", for disk utilisation.

Check once that the token cannot read the project's API keys. This command
prints only the HTTP status, never the response, and should print `403`:

```sh
curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer $(cat /run/secrets/supabase_token)" \
  https://api.supabase.com/v1/projects/<project-ref>/api-keys
```

If it prints `200`, the token can read the project's secret API keys, and
yagura cannot work without that permission: store the token as carefully as
those keys, or do not enable the collector.

Supabase cannot change the permissions of an existing token: to change them,
create a new token and delete the old one.

With this collector on, `/metrics` shows the name, organization and region of
every project the token can see. Turn on the web config (TLS, basic auth) or
keep the port reachable only by your Prometheus server.

The `YaguraSupabaseProjectUnhealthy` alert fires when a project reports
`ACTIVE_UNHEALTHY`, `INIT_FAILED`, `RESTORE_FAILED`, `PAUSE_FAILED` or `UNKNOWN`, or one
service of an active project is not healthy, for 5 minutes. The services of an
`ACTIVE_UNHEALTHY` project do not raise their own alerts. A health call that
keeps failing raises it too while other projects answer, since it reports
every service as unhealthy. `YaguraSupabaseDiskAlmostFull` fires when a
project's disk stays more than 80% full for 15 minutes.

If your scrape configuration adds a target label named `service`, Prometheus
renames yagura's own `service` label to `exported_service` (unless
`honor_labels` is on); the health panel and alert then show the wrong service
name.

#### Database metrics (Supabase Metrics API)

yagura does not read database metrics. Each project serves them, in the
Prometheus format, at
`https://<project-ref>.supabase.co/customer/v1/privileged/metrics` (in beta,
not available for self-hosted Supabase). Scrape that endpoint directly, once
a minute, with HTTP Basic authentication: user `service_role`, password a
secret API key (`sb_secret_…`) created under Project Settings → API Keys.
Ready-made dashboards are in
[`supabase/supabase-grafana`](https://github.com/supabase/supabase-grafana).

**Security note:** a secret API key bypasses Row Level Security and gives
full access to the project's data. Store it like a database superuser
password: in a file readable only by the scraper, never inline in a
configuration file you commit. Create a dedicated key for metrics so you can
revoke it alone, and rotate it on a schedule. This key is separate from the
yagura token: yagura never needs it.

Prometheus:

```yaml
scrape_configs:
  - job_name: supabase
    scrape_interval: 60s
    scheme: https
    metrics_path: /customer/v1/privileged/metrics
    follow_redirects: false
    basic_auth:
      username: service_role
      password_file: /etc/prometheus/supabase-secret-key
    static_configs:
      - targets: ["<project-ref>.supabase.co"]
```

Grafana Alloy:

```alloy
prometheus.scrape "supabase" {
  targets          = [{"__address__" = "<project-ref>.supabase.co"}]
  scheme           = "https"
  metrics_path     = "/customer/v1/privileged/metrics"
  follow_redirects = false
  scrape_interval  = "60s"
  forward_to       = [prometheus.remote_write.default.receiver]

  basic_auth {
    username      = "service_role"
    password_file = "/etc/alloy/supabase-secret-key"
  }
}
```

Redirects are turned off so the key is never sent to another host. Add one
target per project, each with its own secret API key (one scrape job or
component per project).

### Netlify

Watches the last production deploy of every Netlify site and the build
minutes of every team through the Netlify API (`https://api.netlify.com`). It
is enabled when `YAGURA_NETLIFY_TOKEN` (or `YAGURA_NETLIFY_TOKEN_FILE`) is set
and runs two pollers, which share one rate limiter (4 requests per second;
Netlify allows 500 per minute):

- `netlify_deploys` lists every site the token's user can see, 100 per page,
  then asks each site for its 10 latest production deploys and reports the
  first one that is not a build skipped for no content change (see below). A
  site that has never had a production deploy exports no deploy series.
- `netlify_builds` lists the teams of the token's user (called accounts in the
  Netlify API), then reads the build minutes of each team for the current
  billing period.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_NETLIFY_TOKEN` (or `_FILE`) | empty | Personal access token; enables the collector |
| `YAGURA_NETLIFY_DEPLOYS_INTERVAL` | `5m` | Sites and last deploy poll interval (minimum 30s) |
| `YAGURA_NETLIFY_BUILDS_INTERVAL` | `15m` | Build minutes poll interval (minimum 30s) |

Metrics are listed in the [metric reference](#metric-reference). `site` is the
site name and `account` the team slug. `state` is the deploy state exactly as
Netlify reports it; `yagura_netlify_last_deploy_failed` is 1 only for `error`.
When the call of one site or team fails while others answer, yagura logs it,
counts it in `yagura_collector_last_item_errors` and omits the series of that
site or team. When every call fails, the poll fails instead: yagura keeps
serving the last values and `YaguraCollectorFailing` fires after 15 minutes. A
site or team that disappears between the list and the call (HTTP 404) is left
out without an error. A 401 or 403 fails the whole poll;
`yagura_api_requests_total` then shows code `401` for `netlify` when the token
has expired or been revoked, and `403` when its user may not read a resource.

**Token setup.** Netlify personal access tokens have **no scopes**. A token
acts with the full rights of the user who created it, on every team that user
belongs to: for a Developer or an Owner, that includes deploying and changing
site settings and environment variables, and for an Owner, managing the team.
yagura sends only `GET` requests and a
guard enforces this, but anyone who obtains the token gets all of those
rights. To limit the risk:

- where your Netlify plan allows it, create the token as a dedicated team
  member with the least privileged role that can still see the sites, not as
  an Owner, and add that member only to the teams yagura should watch: yagura
  reads every team the user belongs to. Netlify documents no read-only role.
  **Not confirmed:** which role is the least one that can read the sites,
  deploys and build minutes, and whether a member limited to some projects
  sees only those. If the role cannot read one of them, the API answers 403
  and that poller fails;
- create the token under User settings → Applications → Personal access
  tokens → New access token, and give it an expiration date; the
  `YaguraCollectorFailing` alert tells you when it has expired;
- leave "Allow access to my SAML-based Netlify team" unticked unless the
  watched team uses SAML single sign-on;
- store the token in a file (`YAGURA_NETLIFY_TOKEN_FILE`) readable only by
  yagura, rather than inline in `.env`, and never in a configuration file you
  commit. The container image runs as UID 65532: with a Docker secret (see
  `docker-compose.yml`), give the file to that user and keep it private, for
  example `sudo chown 65532 secrets/netlify_token` then
  `sudo chmod 0400 secrets/netlify_token`; do not make it world-readable.

With this collector on, `/metrics` shows the name of every site and the slug
of every team the token can see. Turn on the web config (TLS, basic auth) or
keep the port reachable only by your Prometheus server.

The `YaguraNetlifyLastDeployFailed` alert fires as soon as the last production
deploy of a site is in the `error` state, and resolves as soon as the site's
latest production deploy is in any other state. When the deploy call of the
site fails, its series is omitted and the alert keeps the last known state for
up to 30 minutes; a call that keeps failing raises
`YaguraCollectorPartialFailure` after 30 minutes.

Netlify reports a build it cancels because nothing changed (for example when
an ignore command finds no change) in the `error` state, with an error message
containing "Canceled build due to no content change". yagura does not count
such a skipped build: it reports the latest production deploy that is not
one, so a real failure followed by skipped builds still fires the alert. When
the 10 latest production deploys of a site are all skipped builds,
`yagura_netlify_last_deploy_failed` is 0 and the site's state and creation
time are omitted. Any other deploy in the `error` state counts as failed.

## Budgets and write API

yagura can keep monthly budgets (EUR, excluding VAT) and expose them as
metrics, so that dashboards and alert rules read one source instead of
hard-coded thresholds. Budgets have a scope: `total` (all services),
`scaleway` (Scaleway alone) or `project` (one business project). Per-project
budgets cover Scaleway only.

### Storage

Set `YAGURA_STATE_DIR` to an existing directory. yagura keeps the budgets
there in `budgets.json` (mode 0600), and the
[Scaleway monthly history](#monthly-history) in `scaleway-history.json`. It
rewrites `budgets.json` atomically on each change (temporary
file in the same directory, fsync, rename). On first start, when the file does
not exist, yagura creates it with the budgets of `YAGURA_BUDGET_INITIAL_TOTAL`
and `YAGURA_BUDGET_INITIAL_SCALEWAY` (each optional; no amount is set by
default). Later starts ignore those two variables. The directory must
therefore be writable by yagura on the first start, and always while the API
is on. A file that cannot be read or parsed, has an unknown key, or is not a
regular file stops yagura at startup; it is never reset silently. yagura
reads the file at startup only: stop it before editing the file by hand, or
the next change through the API overwrites the edit. Only one yagura instance
may use a state directory: run the store and the API on a single instance.

With Docker, mount a host directory owned by the container user (UID 65532),
as in the `docker-compose.yml` example. A named volume mounted on a path that
does not exist in the image is created owned by root, and yagura cannot write
to it.

### Business projects

Each Scaleway project seen in the current month's billing data belongs to a
business project. The automatic rule takes the Scaleway project name, cuts it
at the first `-`, trims it, lowercases it and replaces spaces with `-`:
`acme-backups` and `Acme - prod` give `acme`, `Big Suite` gives `big-suite`. When the name is
empty, or the result is not a valid project name (`^[a-z0-9][a-z0-9-]{0,62}$`,
for example a name with an accent or `_`), the project ID is used instead.
So is a name equal to the project ID, which is how the billing API names a
deleted project: the ID is never cut at its first `-`. A manual mapping set
through the API replaces the rule for one project ID.

The project list comes from the billing data the Scaleway collector already
reads: no other Scaleway call or permission is needed. A project with no
consumption in the current month is therefore absent from the list and from
`yagura_scaleway_project_info` until it has some. Early in a month the list
can be short.

### Metrics

```
yagura_budget_amount{scope="total", project="", currency="EUR"} 100
yagura_budget_amount{scope="project", project="acme", currency="EUR"} 40
yagura_scaleway_project_info{project_id="…", project_name="acme-backups", project="acme", mapping="auto"} 1
```

Budgets are read from the store at scrape time: a change shows at the next
scrape, and an unset budget has no series. `yagura_scaleway_consumption_amount`
has no `project` label; join it with `yagura_scaleway_project_info` on
`project_id`, for example the month-to-date cost per business project (amounts
in different currencies are never added together):

```promql
sum by (project, currency) (
  yagura_scaleway_consumption_amount
  * on (job, instance, project_id) group_left (project)
  yagura_scaleway_project_info
)
```

### Write API

Off by default. Set `YAGURA_API_LISTEN_ADDRESS`, `YAGURA_STATE_DIR` and
`YAGURA_API_TOKEN` (or `YAGURA_API_TOKEN_FILE`, re-read on every request) to
turn it on; a missing piece is a startup error. The token must be random and
at least 32 characters long: generate it with `openssl rand -hex 32`. The API has its own listener, without TLS: bind it to
loopback or an internal network only, and never route it from the Internet.
`/metrics` stays on `YAGURA_LISTEN_ADDRESS`.

Every `/api/` request needs `Authorization: Bearer <token>`; without it the
answer is 401 with no detail. Writes are `POST` only. Parameters are read from
the query string, a JSON object body (`Content-Type: application/json`) or a
form body (`application/x-www-form-urlencoded`); a body without a content type
is read as JSON when it starts with `{`, as a form otherwise. An amount is a
number or a string with a dot or a comma and at most two decimals (`300`,
`"300.5"`, `"300,5"`), greater than 0 and at most 100000.

| Method and path | Parameters | Effect |
|---|---|---|
| `GET /api/v1/budgets` | none | List budgets: `scope`, `project`, `amount`, `currency`, `updated_at` |
| `POST /api/v1/budgets/set` | `scope`, `project` when `scope=project`, `amount`, optional `currency` (`EUR` only) | Create or change a budget |
| `POST /api/v1/budgets/unset` | `scope`, `project` when `scope=project` | Remove a budget |
| `GET /api/v1/projects` | none | List Scaleway projects with `project`, `mapping`, `in_billing` (in the current month's billing) and `in_history` (in the [monthly history](#monthly-history)) |
| `POST /api/v1/projects/set` | `project_id`, `project` | Map a Scaleway project manually, also one seen only in the monthly history |
| `POST /api/v1/projects/unset` | `project_id` | Return to the automatic rule |

Status codes: 200 success; 400 invalid parameter (the JSON `error` field says
which); 401 missing or wrong token; 404 unknown path, or nothing to unset; 405
wrong method; 413 body over 16 KiB; 415 unsupported content type; 500 the
change could not be saved (nothing changed). A write answers with the old and
new values, a one-line `message` and a `warnings` list, for example when a
project budget matches no Scaleway project seen in billing:

```json
{"action":"budget_set","scope":"project","project":"acme","currency":"EUR","old_amount":40,"new_amount":60,"message":"budget project acme set to 60 EUR (was 40 EUR)","warnings":[]}
```

Each change writes one log line with the action, the parameters, the old and
new values and the client address. The token is never logged.

The examples below put the token on the command line, where other local users
can see it (`ps`) and the shell history keeps it. On a shared host, store the
header line `Authorization: Bearer <token>` in a 0600 file and pass
`-H @that-file` instead (curl 7.55 or later).

```sh
curl -s -H 'Authorization: Bearer <token>' http://127.0.0.1:10101/api/v1/budgets
curl -s -X POST -H 'Authorization: Bearer <token>' \
  'http://127.0.0.1:10101/api/v1/budgets/set?scope=total&amount=100'
curl -s -X POST -H 'Authorization: Bearer <token>' -H 'Content-Type: application/json' \
  -d '{"scope":"project","project":"acme","amount":"300,5"}' \
  http://127.0.0.1:10101/api/v1/budgets/set
curl -s -X POST -H 'Authorization: Bearer <token>' \
  -d 'scope=project&project=acme' http://127.0.0.1:10101/api/v1/budgets/unset
curl -s -H 'Authorization: Bearer <token>' http://127.0.0.1:10101/api/v1/projects
curl -s -X POST -H 'Authorization: Bearer <token>' \
  'http://127.0.0.1:10101/api/v1/projects/set?project_id=<project-id>&project=acme'
curl -s -X POST -H 'Authorization: Bearer <token>' \
  'http://127.0.0.1:10101/api/v1/projects/unset?project_id=<project-id>'
```

## Target scale

yagura is sized for about 200 entities per provider per instance with the
default intervals. Beyond that, raise the poll intervals. Supabase is the
exception: its health poll makes one call per active project every minute, so
with more than a few dozen active projects, raise
`YAGURA_SUPABASE_HEALTH_INTERVAL`.

## Dashboard and alerts

- `dashboards/yagura-overview.json`: classic Grafana dashboard JSON. Import it
  and pick your Prometheus data source in the `datasource` variable. A Grafana
  data source variable lists one plugin type only, here `prometheus`, so a data
  source of the VictoriaMetrics plugin type does not appear in it. With
  VictoriaMetrics, add a data source of type Prometheus whose URL points at
  VictoriaMetrics (single-node, or vmselect with its `/select/<tenant>/prometheus`
  path): VictoriaMetrics serves the Prometheus query API, and the dashboard
  uses only PromQL.
- `alerts/yagura.rules.yml`: example rules for Prometheus and vmalert. The
  `YaguraExporterDown` rule assumes the scrape job is named `yagura`. Check
  the rules with `make rules` (needs Docker).

### Costs

The "Costs" row of the dashboard shows what the watched services cost per
month, excluding VAT:

- Scaleway is measured: the billing API's amount month to date.
- Gandi is an estimate: the sum of the one-year renewal prices of the domains
  that renew automatically. The monthly totals add one twelfth of it. Gandi
  actually charges each domain when it renews; the "Gandi renewals, next 12
  months" panel shows those amounts by the month in which each registration
  ends.
- Netlify and Supabase are not included: their APIs expose usage, not costs.

The monthly bars are built in Grafana from the month-to-date values already
stored, grouped by calendar month (UTC); yagura stores no series per month,
so the history is as long as your Prometheus or VictoriaMetrics retention.
Months before yagura first polled Gandi prices show Scaleway only. While a
Gandi poll fails, the last prices and autorenew values it read stay in use
(`YaguraCollectorFailing` covers that case). Amounts in different currencies
are never added together.

The budget on the monthly bars is an example (100): edit the thresholds of
the "Monthly cost, all services" and "Scaleway monthly cost" panels to your
budget, and the same value in two alert rules: `YaguraMonthlyCostOverBudget`
(Scaleway alone) and `YaguraTotalMonthlyCostOverBudget` (Scaleway month to
date plus one twelfth of the Gandi estimate, per yagura instance). With
budgets stored by yagura, `yagura_budget_amount` can replace these hard-coded
values (see [Budgets and write API](#budgets-and-write-api)).

## Security model

- Read-only: only `GET` requests are sent to provider APIs. A guard enforces
  this on every request. When an HTTPS proxy is configured, the transport
  opens a `CONNECT` tunnel to that proxy.
- The optional budget write API changes only yagura's own local budget file,
  never anything at a provider. With `YAGURA_STATE_DIR` set and the Scaleway
  collector on, the monthly billing history is also stored there
  (`scaleway-history.json`, mode 0600). It is off by default, listens apart from
  `/metrics`, needs a bearer token compared in constant time, and must stay on
  loopback or an internal network.
- Requests go to the provider APIs you configure. By default yagura also
  requests four public status pages (no credentials, https only); disable this
  with `YAGURA_STATUSPAGE_ENABLED=false` or replace the list with
  `YAGURA_STATUSPAGE_PAGES`. Requests go through `HTTPS_PROXY` / `HTTP_PROXY`
  if the operator sets them.
- Tokens are read from the environment or from `_FILE` files, re-read on each
  poll (the API token on each request), and never logged.
- `/metrics`, `/healthz` and `/` are all behind the optional exporter-toolkit
  web config (TLS, basic auth). With basic auth on, a probe of `/healthz`
  needs the same credentials.

### Minimum token scopes

Each provider section lists the minimum scopes its token needs.
