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
| `yagura_gandi_prepaid_balance` | gauge | `currency` | Prepaid account balance (omitted if the account has no prepaid; omitted with an item error if missing) |
| `yagura_gandi_prepaid_warning_threshold` | gauge | `currency` | Balance below which Gandi sends a warning (omitted if the account has no prepaid or no threshold set; omitted with an item error if it is not a number) |
| `yagura_scaleway_consumption_amount` | gauge | `project_id`, `project_name`, `category`, `currency` | Consumption month to date, as reported by the Scaleway billing API; resets on the 1st |
| `yagura_scaleway_consumption_updated_timestamp_seconds` | gauge | none | Unix time of the billing API's last consumption update (omitted if the API does not send a valid one) |
| `yagura_supabase_project_info` | gauge | `project_ref`, `project_name`, `organization_slug`, `region` | Always 1; carries the project's name, organization and region |
| `yagura_supabase_project_status` | gauge | `project_ref`, `status` | State set: 1 for the project's current status, 0 for every other known status; an unknown status gets its own series with 1 |
| `yagura_supabase_service_healthy` | gauge | `project_ref`, `service` | 1 if the service reports `ACTIVE_HEALTHY`, else 0; active projects only; 0 for every requested service when the project's health call fails |
| `yagura_supabase_disk_size_bytes` | gauge | `project_ref` | Size of the project's disk; active projects only |
| `yagura_supabase_disk_used_bytes` | gauge | `project_ref` | Used space on the project's disk; active projects only |
| `yagura_supabase_disk_available_bytes` | gauge | `project_ref` | Available space on the project's disk; active projects only |
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

Watches the domains held at Gandi and the prepaid account balance through the
Gandi API v5 (`https://api.gandi.net`). It is enabled when `YAGURA_GANDI_TOKEN`
(or `YAGURA_GANDI_TOKEN_FILE`) is set and runs two pollers:

- `gandi_domains` lists every domain and exports its registration end date and
  whether it renews automatically.
- `gandi_billing` reads the billing information and exports the prepaid balance
  and its warning threshold. An account without prepaid exports no prepaid
  series and the poll still succeeds. A prepaid account without a warning
  threshold exports the balance only, also without error; the
  `YaguraGandiPrepaidBalanceLow` alert compares the two series, so it cannot
  fire until a threshold is set in Gandi.

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

With this collector on, `/metrics` shows every domain name the token can see
(only the organization's when `YAGURA_GANDI_SHARING_ID` is set) and the
prepaid balance. Turn on the web config (TLS, basic
auth) or keep the port reachable only by your Prometheus server.

For domains held at other registrars, use a WHOIS-based exporter such as
[`shift/domain_exporter`](https://github.com/shift/domain_exporter). TLS
certificate expiry stays the job of
[blackbox_exporter](https://github.com/prometheus/blackbox_exporter).

### Scaleway

Reads the organization's consumption for the current month through the
Scaleway billing API (`https://api.scaleway.com`). It is enabled when
`YAGURA_SCALEWAY_SECRET_KEY` (or `YAGURA_SCALEWAY_SECRET_KEY_FILE`) is set and
runs one poller, `scaleway_consumption`. The API returns one row per product,
resource or SKU; yagura sums them per project, category and currency, so
products, resources and SKUs never become labels.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_SCALEWAY_SECRET_KEY` (or `_FILE`) | empty | Secret key of an IAM application; enables the collector |
| `YAGURA_SCALEWAY_ORGANIZATION_ID` | empty | Organization ID (UUID) whose consumption is read; required when the collector is enabled |
| `YAGURA_SCALEWAY_INTERVAL` | `1h` | Poll interval (minimum 30s) |

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

**VAT, not confirmed:** the Scaleway console shows consumption excluding
taxes, and the billing API serves taxes from a separate endpoint, which
suggests the same, but we have not confirmed whether the amount the API
returns includes VAT. Compare it with an invoice before you set a budget on
it.

**Key setup.** In the Scaleway console, under IAM:

- create an IAM *application* rather than using a user's API key, so the key
  is not tied to a person;
- attach a policy to the application with a single rule: the
  `BillingReadOnly` permission set, scoped to the organization ("List and read
  access to billing information"). No project permission is needed: the
  consumption rows already carry project IDs and names;
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
IDs and what each one has cost this month. Turn on the web config (TLS, basic
auth) or keep the port reachable only by your Prometheus server.

The `YaguraMonthlyCostOverBudget` alert fires when the month-to-date total of
a currency, for one yagura instance, stays above 100 for an hour; replicas
watching the same organization each raise it. 100 is an example: edit it in `alerts/yagura.rules.yml` to your budget. While polls
fail, yagura keeps serving the last amount it read, so after the 1st the alert
resolves only once a poll succeeds; `YaguraCollectorFailing` covers that case.

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
four at a time, each allowed up to 5 seconds on the Supabase side. With more
than a few dozen active projects, raise `YAGURA_SUPABASE_HEALTH_INTERVAL`.

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
