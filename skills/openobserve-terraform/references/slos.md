# Service level objectives

An SLO measures a service level indicator over a rolling window against a
target. The window divides into slices of `slice_interval_secs`, each
contributing a good/total pair, and the objective is the ratio across the window.

SLOs are **not** enterprise-gated. The server source says so directly:
*"Deliberately NOT enterprise-gated: nothing about SLO measurement is an
enterprise capability."*

```hcl
resource "openobserve_slo" "checkout_availability" {
  folder_id = openobserve_folder.reliability.folder_id # an ALERT folder
  name      = "checkout_availability"

  target              = 99.9    # three nines
  window_secs         = 2592000 # 30 days
  slice_interval_secs = 300     # 5 minutes

  count_sli {
    single_query {
      stream      = "app_logs"
      stream_type = "logs"
      scope       = "service = 'checkout'"
      good_expr   = "status < 500"
    }
  }
}
```

> **SLOs live in alert folders.** There is no SLO folder type. The server source
> is explicit: *"SLOs live in alert folders, there is no `FolderType::Slos`."*

---

## 1. Hard constraints

The provider checks all of these during `plan`.

| Field | Constraint |
|---|---|
| `window_secs` | **only** 604800 (7d), 2592000 (30d), 7776000 (90d) |
| `target` | strictly inside (0, 100), at most 3 decimal places |
| indicator | exactly one of `count_sli`, `time_slice_sli`, `alert_sli` |
| `absent_is_bad` | ungrouped objectives only |

> `window 86400s is not one of the supported rolling windows (7d, 30d, 90d)`

A 100% target is rejected because it leaves a zero error budget, which makes
every burn rate either 0 or infinite.

---

## 2. The wire format

This is the most intricate shape in the API, and worth understanding because the
error messages only make sense once you have seen it. The indicator is an
**adjacently tagged enum**:

```json
{
  "name": "checkout_availability",
  "sli_type": "count",
  "config": { ... },
  "window_secs": 2592000,
  "slice_interval_secs": 300,
  "target": 99.9,
  "group_by": ["region"],
  "enabled": true
}
```

For `count`, `config` contains **another** adjacent tag, because the Rust
variant is a struct variant with a `source` field:

```json
"config": {
  "source": {
    "mode": "single_query",
    "query": { "stream": "app_logs", "stream_type": "logs", "good_expr": "status < 500" }
  }
}
```

Two levels: `sli_type`/`config`, then `mode`/`query` nested under `source`.
Omitting the `source` wrapper produces:

> `Failed to deserialize the JSON body into the target type: missing field 'source'`

For `time_slice` and `alert`, `config` holds the fields directly with no wrapper.

The provider handles all of this. It matters when reading an exported SLO or
debugging a raw API call.

---

## 3. Indicator types

Exactly one block is required. They answer different questions.

### `count_sli`: good events over total

The natural choice for availability. Three sources, in descending order of
preference.

**`single_query`** counts both numerator and denominator in one scan, so they
are provably drawn from the same rows. Use this unless you cannot.

```hcl
count_sli {
  single_query {
    stream      = "app_logs"
    stream_type = "logs"
    scope       = "service = 'checkout'" # denominator filter; omit for all rows
    good_expr   = "status < 500"         # numerator predicate, mandatory
  }
}
```

**`promql`** exists because pre-aggregated counters have no rows for a
`good_expr` to classify: "good" only exists as arithmetic between series, and
correct counter arithmetic is `increase()`, which SQL over raw samples cannot
express. Use a range selector equal to the slice interval, since the evaluator
samples at slice ends.

```hcl
count_sli {
  promql {
    good  = "sum(increase(http_requests_total{status!~\"5..\"}[5m]))"
    total = "sum(increase(http_requests_total[5m]))"
  }
}
```

**`dual_query`** runs separate good and total queries. Weaker: two scans that
cannot be proven to have seen the same instant. It exists for imported
definitions that cannot be folded into one query.

```hcl
count_sli {
  dual_query {
    good {
      stream      = "app_logs"
      stream_type = "logs"
      sql         = "SELECT histogram(_timestamp, '5 minute') AS slice_start, count(*) AS zo_slo_value FROM \"app_logs\" WHERE status < 500 GROUP BY slice_start"
    }
    total {
      stream      = "app_logs"
      stream_type = "logs"
      sql         = "SELECT histogram(_timestamp, '5 minute') AS slice_start, count(*) AS zo_slo_value FROM \"app_logs\" GROUP BY slice_start"
    }
  }
}
```

Each query must project `slice_start`, every `group_by` column, and exactly one
numeric `zo_slo_value`.

> **`histogram()` needs a quoted interval literal.** `histogram(_timestamp)`
> alone is rejected:
>
> `good_query has an invalid projection: histogram() needs a quoted interval literal`
>
> Write `histogram(_timestamp, '5 minute')`.

### `time_slice_sli`: each slice is good or bad

The natural choice for latency, where what matters is whether a percentile
stayed under a bound rather than counting individual events.

```hcl
time_slice_sli {
  stream         = "app_logs"
  stream_type    = "logs"
  query_language = "sql" # sql | prom_ql
  query          = "SELECT approx_percentile_cont(duration_ms, 0.95) AS zo_slo_value FROM \"app_logs\""
  comparator     = "<"   # orderable only: > >= < <=
  threshold      = 300
  absent_is_bad  = false
}
```

Only orderable comparators are accepted. A slice with no value is a **gap**, not
a failure, so equality has no meaning here.

**`absent_is_bad`** inverts that for freshness objectives, where a silent
pipeline *is* the failure:

```hcl
time_slice_sli {
  # ...
  absent_is_bad = true
}
```

It changes only the meaning of a **successful** query's empty result. A failed
query still writes nothing and coverage falls, so a search outage freezes the
objective rather than firing it.

Ungrouped objectives only. Gap fill cannot see a group missing from an entire
pass, so a grouped freshness objective would freeze instead of firing for
exactly the failure it watches for. The provider rejects that combination during
`plan`.

### `alert_sli`: derived from an alert

Slices where the alert was firing count as bad.

```hcl
alert_sli {
  alert_id = openobserve_alert.high_error_rate.alert_id
}
```

---

## 4. Grouping

`group_by` splits an objective into independent per-group measurements. Each
group gets its own budget.

```hcl
resource "openobserve_slo" "by_region" {
  name     = "checkout_availability_by_region"
  group_by = ["region"]
  # ...
}
```

Grouping is defined once, on the objective, deliberately: it is the single
source of truth for slice identity, and every alert on the objective inherits
those groups. There is no second place to configure it.

---

## 5. Enabling and moving

Both are separate endpoints, not fields on the update body:

- `PUT /api/{org}/slos/{id}/enable?value=true|false`
- `POST /api/{org}/slos/move` with `{"slo_ids": [...], "dst_folder_id": "..."}`

The provider calls them when `enabled` or `folder_id` changes, so from HCL both
simply work. This matters when debugging a raw API script that only calls
update and appears to lose the change.

---

## 6. Reading the measurement

```hcl
data "openobserve_slo" "checkout" {
  name = "checkout_availability"
}

output "budget_remaining_pct" {
  value = data.openobserve_slo.checkout.status.error_budget_remaining
}
```

`status` fields:

| Field | Meaning |
|---|---|
| `coverage` | Fraction of the window actually measured, 0 to 1 |
| `no_data` | **Frozen**: coverage below the floor |
| `sli` | Measured indicator as a percentage |
| `error_budget_remaining` | Percentage unspent; **goes negative** when overspent |
| `burn_rate` | Multiples of the budget-neutral rate |
| `time_to_exhaust_secs` | Null when burn is at or below neutral |
| `good`, `total`, `covered_slices`, `computed_at` | Raw counters |

### Three states, not two

`status` is `null` before the first evaluation pass. `no_data` is true when the
objective is frozen, which is neither healthy nor breached. **Neither is zero.**
A brand-new objective is not 0% available, and a dashboard that renders it that
way is lying.

This is a live observation: a freshly created SLO with no ingested data returns
`no_data = true`, `coverage = 0`, and every derived figure null.

```hcl
output "frozen" {
  value = [
    for s in data.openobserve_slos.all.slos :
    s.name if s.status != null && s.status.no_data
  ]
}
```

`error_budget_remaining` is deliberately unclamped. After burning 180% of the
budget, `-80%` is the number an operator needs to see.

### `definition_generation`

Bumped by the server on every edit that changes what a slice *means*. A bump
restarts measurement, because slices computed under the old definition are no
longer comparable. Read-only.

---

## 7. SLO alerts

An SLO alert is the **fourth threshold family** on `openobserve_alert`. It reads
a precomputed objective rather than running a query, so it costs nothing to
evaluate and fires on the same numbers the SLO page shows.

### Error budget

```hcl
resource "openobserve_alert" "budget_burned" {
  name         = "checkout-budget-burned"
  stream_type  = "logs"
  stream_name  = "app_logs"
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "slo"

    slo_condition {
      slo_id   = openobserve_slo.checkout_availability.slo_id
      kind     = "error_budget"
      operator = ">" # ascending only: > or >=
      critical = 90  # 90% of the budget consumed
      warning  = 75
    }
  }

  # No threshold or operator: an SLO alert has no count gate.
  trigger_condition {
    period    = 5
    frequency = 5
  }
}
```

### Burn rate

Fires on how fast the budget is being spent, in multiples of the budget-neutral
rate. It evaluates in two windows and fires only when **both** exceed the
threshold, which is what stops a brief spike paging.

```hcl
slo_condition {
  slo_id            = openobserve_slo.checkout_availability.slo_id
  kind              = "burn_rate"
  operator          = ">"
  critical          = 14.4 # burns a 30-day budget in about two days
  long_window_secs  = 3600 # 1h to 48h, no longer than the SLO window
  short_window_secs = 900
}
```

### The single most important rule

**An SLO alert has no count gate.** For every other family
`trigger_condition.threshold` means "for at least N groups or series". Here it is
unused, and the server **rejects** a non-default value rather than ignoring it:

> `SLO alerts have no count gate; leave the trigger threshold and operator at their defaults`

The server source explains the choice: *"silently ignoring config is how the D13
mistake happened."*

So omit `threshold` and `operator` from `trigger_condition` entirely. The
provider errors during `plan` if they are set, and sends the server defaults
when they are not.

### Both windows are required

> `burn-rate alerts require both a long and a short window`

The field documentation says the short window defaults to long divided by 12,
per the Google SRE workbook, but the server does not implement that default. It
also enforces a minimum:

> `short window 300s must be at least 600s (2 slices); a one-slice window has coverage 0 or 1, so a single gap freezes the alert`

The provider deliberately does **not** derive the short window. The minimum
depends on the objective's `slice_interval_secs`, so any derived value could be
rejected: `3600 / 12 = 300s` fails against a 300s slice interval, which is the
common case. Set both explicitly.

### Windows are burn-rate only

Setting `long_window_secs` or `short_window_secs` on an `error_budget` alert is
rejected during `plan`. An error budget alert measures over the SLO's own
window, so the fields would silently do nothing.

### No per-group SLO alerting yet

> `per-group alerting (multi_alert) is not yet supported for SLO alerts; alert on the rollup instead`

`slo_condition.multi_alert` exists and is in the server's own struct, but
validation rejects it today. The provider keeps the attribute without a
plan-time block so it starts working the moment the server does. Per-group
alerting works today on aggregation and PromQL alerts.

---

## 8. Everything the provider checks before applying

- `window_secs` is 604800, 2592000, or 7776000
- `target` is greater than 0 and strictly below 100
- Exactly one indicator block, and exactly one `count_sli` source
- `absent_is_bad` is not combined with `group_by`
- A burn-rate alert has both windows
- Window fields are not set on an `error_budget` alert
- An SLO alert does not set `trigger_condition.threshold` or `operator`

## 9. Import

```bash
terraform import openobserve_slo.checkout_availability default/2fXkZ8QlmNbYcV1pR3sT
```

```hcl
data "openobserve_slos" "all" {}

output "slo_ids" {
  value = { for s in data.openobserve_slos.all.slos : s.name => s.slo_id }
}
```
