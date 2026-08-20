# Alerts

`openobserve_alert` covers scheduled and real-time alerts across four query
types. Composite alerts are a separate resource; see `composite-alerts.md`.

Alerts use the v2 API (`/api/v2/{org}/alerts`).

An alert has two halves:

- `query_condition` says **what to measure**
- `trigger_condition` says **when that measurement is a problem**

Everything else, from destinations and templates to tags, hangs off those two.

---

## 1. Templates and destinations come first

An alert cannot exist without somewhere to send its notification.

```hcl
resource "openobserve_alert_template" "slack" {
  name = "slack"
  type = "http" # http | email | sns
  body = jsonencode({
    text = "{alert_name} fired on {stream_name} at {alert_start_time}\n{alert_url}"
  })
}

resource "openobserve_alert_destination" "slack" {
  name     = "slack-platform"
  type     = "http"
  url      = var.slack_webhook_url
  method   = "post" # post | put | get
  template = openobserve_alert_template.slack.name
}
```

> **A destination without a template becomes a pipeline destination and cannot
> be used by an alert.** Always create the pair.

### Template placeholders

`{alert_name}`, `{stream_name}`, `{org_name}`, `{alert_start_time}`,
`{alert_end_time}`, `{alert_url}`, `{rows}`.

`{rows}` expands the alert's own `row_template` once per matched record, so the
per-row formatting lives on the alert and the envelope lives on the template.

### Destination requirements by type

The provider checks these during `plan` rather than letting the apply fail.

| `type` | Required |
|---|---|
| `http` | `url` |
| `email` | `emails`, each belonging to a user in the organization |
| `sns` | `sns_topic_arn` and `aws_region` |

### `is_default` is server-decided

OpenObserve marks a template as the organization default when it is the only
one, whatever you set. The provider treats the field as computed so this does
not become permanent drift.

### Prebuilt templates

The server ships read-only templates (`prebuilt_slack`, `prebuilt_pagerduty`,
`prebuilt_msteams`, and others). Reference them rather than recreating:

```hcl
data "openobserve_alert_template" "slack" {
  name = "prebuilt_slack"
}
```

---

## 2. The four query types

### `sql`

An aggregate compared against a threshold.

```hcl
query_condition {
  type = "sql"
  sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE level = 'error'"
}
```

### `custom`

UI-style conditions as a JSON document. Use `jsonencode()`.

```hcl
query_condition {
  type = "custom"
  conditions = jsonencode({
    or = [
      { column = "status", operator = "=", value = "failed", ignore_case = false },
    ]
  })
}
```

Both the flat form above and the nested grouped form are accepted:

```hcl
conditions = jsonencode({
  filterType      = "group"
  logicalOperator = "AND"
  conditions = [
    { column = "status", operator = ">=", value = 500, ignore_case = false },
    { column = "service", operator = "=", value = "checkout", ignore_case = false },
  ]
})
```

`custom` is also the family that supports `aggregation`; see section 5.

### `promql`

```hcl
query_condition {
  type   = "promql"
  promql = "avg by (instance) (rate(node_cpu_seconds_total{mode!=\"idle\"}[5m]))"

  promql_condition {
    column   = "value"
    operator = ">"
    value    = "0.9"
  }
}
```

`column` is almost always `"value"`, which is the PromQL result value.

### `slo`

Reads a precomputed objective instead of running a query. Covered fully in
`slos.md`, because its rules differ from every other family.

```hcl
query_condition {
  type = "slo"

  slo_condition {
    slo_id   = openobserve_slo.checkout_availability.slo_id
    kind     = "error_budget"
    operator = ">"
    critical = 90
    warning  = 75
  }
}
```

---

## 3. Threshold value encoding

`value` is a string in HCL, and the provider decides the JSON type: anything
that parses as a number is sent as a JSON number, anything else as a JSON
string.

| HCL | JSON sent |
|---|---|
| `value = "100"` | `100` |
| `value = "1.5"` | `1.5` |
| `value = "error"` | `"error"` |

It round-trips unchanged either way, so this never shows as drift.

`ignore_case = true` makes a string comparison case-insensitive. It applies to
`promql_condition`, to `aggregation.having`, and to conditions inside a `custom`
`conditions` document.

> **An aggregation's `having.value` must be numeric.** An aggregation aggregates
> numbers, so a string threshold is rejected:
>
> `Invalid aggregation warning value: aggregation threshold (having.value) is not numeric`

---

## 4. Trigger condition

```hcl
trigger_condition {
  period            = 15        # lookback window, minutes
  operator          = ">="      # = != > >= < <= Contains NotContains
  threshold         = 100
  warning_threshold = 50
  notify_on_warning = false     # record warnings without paging
  frequency         = 5         # evaluate every N minutes
  frequency_type    = "minutes" # minutes | cron
  cron              = ""
  timezone          = "UTC"
  silence           = 60        # stay quiet N minutes after firing
  align_time        = true      # snap windows to period boundaries
  tolerance_in_secs = 0
}
```

For cron scheduling:

```hcl
trigger_condition {
  frequency_type = "cron"
  cron           = "0 */10 * * * *" # SIX fields
  timezone       = "America/New_York"
  period         = 10
  operator       = ">="
  threshold      = 1
  silence        = 30
}
```

> **Cron expressions have six fields, seconds first:**
> `sec min hour day-of-month month day-of-week`.
>
> A five-field cron fails, and confusingly so. The server replaces a **leading**
> `*` with the current second, to spread scheduling load, so `*/10 * * * *` is
> rewritten to `4 /10 * * * *` and then fails to parse:
>
> `{"code":400,"message":"4 /10 * * * *\n           ^\n"}`
>
> Write `0 */10 * * * *` to run every ten minutes on the minute. Writing
> `* */10 * * * *` is also valid and opts into the load-spreading rewrite, at
> the cost of the stored value differing from what you wrote.

`is_real_time = true` on the alert evaluates as matching data arrives rather
than on a schedule. A real-time alert cannot be a composite child, because it
produces no scheduled state for a composite to read.

---

## 5. Warning thresholds

Every family supports a second, less severe level that shares the operator of
the critical one. Omit it for a single-level alert.

| Family | Critical | Warning |
|---|---|---|
| Threshold | `trigger_condition.threshold` | `trigger_condition.warning_threshold` |
| Aggregation | `aggregation.having.value` | `aggregation.warning_value` |
| PromQL | `promql_condition.value` | `promql_warning_value` |
| SLO | `slo_condition.critical` | `slo_condition.warning` |

The warning must be strictly **less severe** than the critical, which is
operator-dependent: `>` needs a smaller value, `<` a larger one. `=`, `!=` and
`contains` reject a warning outright, having no ordering.

`notify_on_warning = false` records warnings in history and the UI without
paging. Useful for "wake me only on critical".

> **Trap.** `warning_threshold` is **rejected** on aggregation alerts:
>
> `warning_threshold is not supported on aggregation alerts: the count threshold is coverage, not severity, use aggregation.warning_value instead`

### Two alerts that should be one

A common pattern in hand-written alert sets is a "high" alert at 80 and a
"critical" alert at 90 with otherwise identical queries. Both fire at 91, so the
same condition pages twice. That is one alert with two levels:

```hcl
query_condition {
  type   = "promql"
  promql = "..." # identical in both original alerts

  promql_condition {
    column   = "value"
    operator = ">="
    value    = "90"
  }

  promql_warning_value = 80
}
```

---

## 6. Simple alerts and multi-alerts

This is the distinction most worth understanding.

A **simple alert** collapses the whole query to one verdict. One alert, one
state, one notification, no matter how many services or series are involved.

A **multi-alert** evaluates each group independently. Every group gets its own
level, its own state row, and its own notifications. One breaching service pages
about that service, instead of a page saying "something, somewhere, is broken".

Each family defines a group differently, so each has its own switch.

### Aggregation: the group is the `group_by` column set

```hcl
query_condition {
  type = "custom"

  aggregation {
    group_by      = ["service"]
    function      = "p95" # avg min max sum count median p50 p75 p90 p95 p99
    multi_alert   = true
    warning_value = 500

    having {
      column   = "duration_ms"
      operator = ">"
      value    = "1000"
    }
  }
}
```

Aggregation multi-alerts require a non-empty `group_by` and an orderable
operator.

### PromQL: the group is each returned series' label set

```hcl
query_condition {
  type               = "promql"
  promql             = "sum by (pod) (rate(errors_total[5m]))"
  promql_multi_alert = true

  promql_condition {
    column   = "value"
    operator = ">"
    value    = "10"
  }
}
```

The grouping comes from the expression's own `by (...)` clause. There is
deliberately no separate label picker, because one could only ever disagree with
the query.

### SQL alerts with GROUP BY are aggregations in disguise

A SQL alert of the form

```sql
SELECT count(*) AS n, namespace FROM "k8s_events" WHERE ... GROUP BY namespace HAVING n > 0
```

collapses to a single verdict, so twelve breaching namespaces produce one
notification. Rewriting it as a `custom` alert with an `aggregation` block and
`multi_alert = true` gives one notification per namespace.

### SLO: not yet supported

`slo_condition.multi_alert` exists and is in the server's own struct, but the
server rejects it today:

> `per-group alerting (multi_alert) is not yet supported for SLO alerts; alert on the rollup instead`

The provider keeps the attribute without a plan-time block so it starts working
the moment the server does.

---

## 7. Deduplication

Collapses repeated firings of the same underlying issue.

```hcl
deduplication {
  enabled             = true
  fingerprint_fields  = ["service", "error_code"]
  time_window_minutes = 30
}
```

Leave `fingerprint_fields` empty and the server infers them from the query:
condition fields for `custom`, GROUP BY columns for `sql`, label dimensions for
`promql`.

---

## 8. Folders

Alerts live in alert folders, shared with SLOs.

```hcl
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Reliability"
}

resource "openobserve_alert" "example" {
  folder_id = openobserve_folder.reliability.folder_id
  # ...
}
```

> **The update endpoint does not move an alert.** `update_alert` passes `None`
> for the folder, so a folder change there is silently dropped. Moving requires
> `PATCH /api/v2/{org}/alerts/move`. The provider issues this automatically when
> `folder_id` changes, so from HCL it simply works, but this is why a hand-rolled
> API script that only calls update appears to lose folder changes.

---

## 9. Alert names

Rejected, not rewritten, for characters OpenFGA cannot carry: `:`, `#`, `?`,
whitespace, `'`, `"`, `%`, `&` (regex `[:#?\s'"%&]+`), and separately `/`.

Parentheses, hyphens, and dots are fine. Note this differs from role and group
names, which the server silently rewrites instead.

---

## 10. Remaining fields

| Field | Notes |
|---|---|
| `row_template` | Per-record format string expanded into `{rows}` |
| `row_template_type` | `String` (default) or `Json` |
| `context_attributes` | Key/value map available to the template |
| `tz_offset` | Minutes, negative west of UTC |
| `priority` | 1 (most urgent) to 5. Display metadata only; does not affect firing |
| `tags` | Selection tags, for example `prod`, `service:checkout` |
| `creates_incident` | Route through the incident system instead of notifying directly |
| `workflows` | Workflow IDs to trigger |
| `enabled` | Disabled alerts stay configured but never evaluate |

> **Every alert needs a destination or a workflow:**
>
> `Alert destination or workflows is required`

---

## 11. A complete alert

```hcl
resource "openobserve_alert" "high_error_rate" {
  name         = "high-error-rate"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]
  description  = "Error volume above the acceptable rate"

  priority = 2
  tags     = ["prod", "service:checkout"]

  row_template = "service={service} count={total}"

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE level = 'error'"
  }

  trigger_condition {
    period            = 15
    operator          = ">="
    threshold         = 500
    warning_threshold = 100
    notify_on_warning = false
    frequency         = 5
    silence           = 60
  }

  deduplication {
    enabled             = true
    time_window_minutes = 30
  }
}
```

## 12. Reading alerts back

```hcl
data "openobserve_alerts" "all" {}

output "alert_ids" {
  value = { for a in data.openobserve_alerts.all.alerts : a.name => a.alert_id }
}

data "openobserve_alert" "one" {
  name = "high-error-rate"
}
```

The list includes composite alerts, distinguished by `alert_type == "composite"`.
