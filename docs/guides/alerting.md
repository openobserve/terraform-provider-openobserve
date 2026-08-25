---
page_title: "Alerting"
subcategory: "Guides"
description: |-
  Every alert query type, warning thresholds, and the difference between a simple alert and a multi-alert.
---

# Alerting

An alert has two halves. `query_condition` says *what to measure*, and
`trigger_condition` says *when that measurement counts as a problem*. Everything
else, from destinations and templates to tags, hangs off those two.

## Templates and destinations

A destination is where a notification goes. A template renders it. A destination
without a template is treated as a pipeline destination and cannot be used by an
alert, so pair them:

```hcl
resource "openobserve_alert_template" "slack" {
  name = "slack"
  type = "http"
  body = jsonencode({
    text = "{alert_name} fired on {stream_name}\n{alert_url}"
  })
}

resource "openobserve_alert_destination" "slack" {
  name     = "slack-platform"
  type     = "http"
  url      = var.slack_webhook_url
  template = openobserve_alert_template.slack.name
}
```

Templates support `{alert_name}`, `{stream_name}`, `{org_name}`,
`{alert_start_time}`, `{alert_end_time}`, `{alert_url}`, and `{rows}` for the
matched records.

Email destinations only accept addresses belonging to users in the organization.
SNS destinations need both `sns_topic_arn` and `aws_region`. The provider checks
these combinations during `plan`, rather than letting the apply fail.

~> The template is what makes this an *alert* destination. The same endpoint
stores pipeline destinations, which carry no template and cannot be used by an
alert. Use `openobserve_pipeline_destination` for those; see the
[pipelines guide](pipelines).

## The four query types

### `sql`: an aggregate compared against a threshold

```hcl
query_condition {
  type = "sql"
  sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE level = 'error'"
}
```

### `custom`: UI-style conditions, optionally aggregated

`conditions` is a JSON document, so build it with `jsonencode()`:

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

### `promql`: a metrics expression

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

### `slo`: an objective's error budget or burn rate

An SLO alert reads a precomputed objective rather than running its own query, so
it costs nothing to evaluate and fires on the same numbers the SLO page shows.
See the [SLO guide](slos).

```hcl
query_condition {
  type = "slo"

  slo_condition {
    slo_id   = openobserve_slo.checkout_availability.slo_id
    kind     = "error_budget"
    operator = ">"
    critical = 90 # 90% of the budget consumed
    warning  = 75
  }
}
```

## Threshold values

`value` on a condition is a string, and the provider decides how to encode it:
anything that parses as a number is sent as a JSON number, anything else as a
JSON string. `value = "100"` is the number 100; `value = "error"` is the string
`"error"`. It round-trips unchanged either way.

## Warning thresholds

Every alert family supports a second, less severe level that shares the
operator of the critical one. Omit it for a single-level alert.

| Family | Critical | Warning |
|---|---|---|
| Threshold | `trigger_condition.threshold` | `trigger_condition.warning_threshold` |
| Aggregation | `aggregation.having.value` | `aggregation.warning_value` |
| PromQL | `promql_condition.value` | `promql_warning_value` |
| SLO | `slo_condition.critical` | `slo_condition.warning` |

The warning must be strictly *less severe* than the critical one, which depends
on the operator: `>` needs a smaller value, `<` a larger one.

`notify_on_warning = false` records warnings in history and the UI without
paging. Useful for "wake me only on critical".

```hcl
trigger_condition {
  period            = 15
  operator          = ">="
  threshold         = 500
  warning_threshold = 100
  notify_on_warning = false
  frequency         = 5
}
```

## Simple alerts and multi-alerts

This is the distinction worth understanding.

A **simple alert** collapses the whole query to one verdict. One alert, one
state, one notification, no matter how many services or series are involved.

A **multi-alert** evaluates each group independently. Every group gets its own
level, its own state row, and its own notifications. One breaching service pages
about that service, instead of pages saying "something, somewhere, is broken".

Which knob turns it on depends on the family, because each defines a group
differently:

```hcl
# Aggregation: the group is the group_by column set.
query_condition {
  type = "custom"

  aggregation {
    group_by    = ["service"]
    function    = "p95"
    multi_alert = true

    having {
      column   = "duration_ms"
      operator = ">"
      value    = "1000"
    }
  }
}
```

```hcl
# PromQL: the group is each returned series' full label set, chosen by the
# expression's own `by (…)` clause. There is no separate label picker, because
# one could only ever disagree with the query.
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

```hcl
# SLO: the group is the objective's own group_by. Requires a grouped SLO.
slo_condition {
  slo_id      = openobserve_slo.by_region.slo_id
  kind        = "burn_rate"
  operator    = ">"
  critical    = 14.4
  multi_alert = true
}
```

Aggregation multi-alerts need a non-empty `group_by` and an orderable operator.

## Composite alerts

Everything above describes one alert watching one thing. A **composite alert**
watches other alerts: it combines their current states through a boolean
expression and fires when that expression becomes true.

This is not the same as writing a bigger query. "The error rate is high" pages
at 3am whether or not anyone can act on it. "The error rate is high *and* a
deploy went out in the last thirty minutes" is a page with an obvious next step.
Both facts already exist as alerts; a composite just says how they combine.

```hcl
resource "openobserve_composite_alert" "bad_deploy" {
  name         = "checkout-bad-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.slack.name]

  expression = "{${openobserve_alert.error_rate.alert_id}} && {${openobserve_alert.recent_deploy.alert_id}}"

  silence = 30
}
```

A composite never re-runs its children's queries. It reads the state they
already computed, so it costs nothing to evaluate and cannot disagree with what
the children's own pages said. That is also why it has no schedule of its own:
it is re-evaluated when a child changes state. `openobserve_composite_alert`
accepts no `period`, `frequency`, `threshold`, `stream_name` or
`query_condition`, and the server rejects them rather than ignoring them.
`silence` is the only scheduling attribute.

### The expression

Operands are brace-wrapped alert IDs. The operators are `&&`, `||` and `!`,
with `&&` binding tighter than `||` and `!` tighter than both, as in most
languages. Parentheses override that.

```hcl
expression = "{${openobserve_alert.errors.alert_id}} && !{${openobserve_alert.maintenance.alert_id}}"
```

Interpolating each child's `alert_id` is also what makes the dependency
explicit, so Terraform creates children before the composite and destroys them
after. That ordering matters: the server refuses to delete an alert while a
composite still names it.

An expression must reference between 2 and 10 distinct children and may not
name the same child twice. The provider checks syntax, operand count and
duplicates during `plan`, so those come back as a diagnostic on the offending
line rather than as an apply-time error.

The server does not store the expression as written. It reparses and stores a
fully parenthesized rewrite, so `{a} && {b}` is stored as `({a} && {b})`. The
provider compares the two as expressions rather than as text, so your spelling
is preserved and equivalent parenthesization is never reported as drift.

### Which children are eligible

Scheduled alerts, SLO alerts, and other composites. Real-time alerts are not:
they have no scheduled state for a composite to read, and are rejected with
*child alert ... is not eligible for composite evaluation*.

A composite may reference another composite, but only one level deep. A
composite of composites of composites is rejected, as is any reference cycle.

### Warnings and stale children

Two attributes decide what a child contributes.

`warning_counts_as_firing` (default `true`) decides whether a child sitting at
warning counts as true. Set it to `false` for a composite that should only react
to children at critical.

`stale_child_policy` decides what a child contributes once it goes stale,
meaning it has not been evaluated within three times its own cadence:

| Value | A stale child |
|---|---|
| `use_last_state` (default) | keeps contributing whatever it last reported |
| `treat_as_false` | stops satisfying the expression |
| `treat_as_true` | satisfies the expression |

`treat_as_false` keeps a broken child from holding a composite firing forever.
`treat_as_true` is the fail-safe choice for absence-of-heartbeat patterns, where
a child going quiet is itself the signal. Choosing between them is a decision
about which failure you would rather have, and the default quietly picks
"trust the frozen value", so it is worth setting deliberately.

### Inspecting one

The data source reports the state the composite actually read, which is how you
answer why it is or is not firing:

```hcl
data "openobserve_composite_alert" "bad_deploy" {
  name = "checkout-bad-deploy"
}

output "children_currently_true" {
  value = [for c in data.openobserve_composite_alert.bad_deploy.children : c.name if c.truth]
}
```

And when a destroy is refused, `openobserve_composite_alert_references` says
what is holding the alert, including composites created outside Terraform:

```hcl
data "openobserve_composite_alert_references" "error_rate" {
  alert_id = openobserve_alert.error_rate.alert_id
}
```

~> Composite mutation can be turned off server-side with
`ZO_ALERT_COMPOSITE_WRITES_ENABLED=false`, and composites are unavailable
entirely on super-cluster deployments. Both surface as a clear error on apply.

## Scheduling

By default an alert runs every `frequency` minutes over a `period`-minute
lookback. For cron scheduling set `frequency_type` and a `timezone`:

```hcl
trigger_condition {
  frequency_type = "cron"
  cron           = "*/10 * * * *"
  timezone       = "UTC"
  period         = 10
  operator       = ">="
  threshold      = 1
  silence        = 30
}
```

`silence` is how many minutes the alert stays quiet after firing. `align_time`
snaps each query window to period boundaries.

Setting `is_real_time = true` evaluates as matching data arrives instead of on a
schedule.

## Reducing noise

`deduplication` collapses repeated firings of the same underlying issue:

```hcl
deduplication {
  enabled             = true
  fingerprint_fields  = ["service", "error_code"]
  time_window_minutes = 30
}
```

Leave `fingerprint_fields` empty and the server infers them from the query: the
condition fields for `custom`, the GROUP BY columns for `sql`, and the label
dimensions for `promql`.

## Folders

Alerts live in folders, and so do SLOs; they share the same namespace:

```hcl
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Reliability"
}

resource "openobserve_alert" "example" {
  folder_id = openobserve_folder.reliability.folder_id
  # …
}
```

Changing `folder_id` on an existing alert moves it. The update endpoint
deliberately leaves the folder alone, so the provider issues a separate move.

## Full examples

```terraform
# A scheduled SQL alert: fire when errors exceed 100 in a 15 minute window.
resource "openobserve_alert" "high_error_rate" {
  name        = "high-error-rate"
  stream_type = "logs"
  stream_name = openobserve_stream.app_logs.name
  description = "Error volume is above the normal baseline"

  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(*) AS total FROM \"app_logs\" WHERE level = 'error'"
  }

  trigger_condition {
    period            = 15
    operator          = ">="
    threshold         = 100
    warning_threshold = 50
    frequency         = 5
    silence           = 60
  }
}

# A real-time alert: fire as soon as a matching record arrives.
resource "openobserve_alert" "payment_failure" {
  name         = "payment-failure"
  stream_type  = "logs"
  stream_name  = "payments"
  is_real_time = true

  destinations = [openobserve_alert_destination.pagerduty.name]
  priority     = 1
  tags         = ["prod", "service:payments"]

  query_condition {
    type = "custom"
    conditions = jsonencode({
      or = [
        { column = "status", operator = "=", value = "failed", ignore_case = false },
      ]
    })
  }

  trigger_condition {
    period    = 1
    operator  = ">="
    threshold = 1
    frequency = 1
  }
}

# An aggregation alert on a cron schedule, evaluated per service.
resource "openobserve_alert" "slow_service" {
  name        = "slow-service"
  stream_type = "logs"
  stream_name = openobserve_stream.app_logs.name
  folder_id   = openobserve_folder.platform_alerts.folder_id

  destinations = [openobserve_alert_destination.oncall_email.name]

  query_condition {
    type = "custom"

    aggregation {
      group_by      = ["service"]
      function      = "p95"
      multi_alert   = true
      warning_value = 500

      having {
        column   = "duration_ms"
        operator = ">"
        value    = "1000"
      }
    }
  }

  trigger_condition {
    frequency_type = "cron"
    cron           = "*/10 * * * *"
    timezone       = "UTC"
    period         = 10
    operator       = ">="
    threshold      = 1
    silence        = 30
  }
}

# A PromQL alert on a metrics stream.
resource "openobserve_alert" "cpu_saturation" {
  name        = "cpu-saturation"
  stream_type = "metrics"
  stream_name = "node_cpu_seconds_total"

  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type   = "promql"
    promql = "avg by (instance) (rate(node_cpu_seconds_total{mode!=\"idle\"}[5m]))"

    promql_condition {
      column   = "value"
      operator = ">"
      value    = "0.9"
    }
  }

  trigger_condition {
    period    = 5
    frequency = 5
    operator  = ">="
    threshold = 1
  }
}

# An SLO alert reads a precomputed objective rather than running a query, so it
# costs nothing to evaluate and fires on the same numbers the SLO page shows.
resource "openobserve_alert" "budget_burned" {
  name         = "checkout-budget-burned"
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "slo"

    slo_condition {
      slo_id   = openobserve_slo.checkout_availability.slo_id
      kind     = "error_budget"
      operator = ">"
      critical = 90 # 90% of the budget consumed
      warning  = 75
    }
  }

  # No threshold or operator here: an SLO alert has no count gate, and the
  # server rejects one rather than ignoring it.
  trigger_condition {
    period    = 5
    frequency = 5
  }
}

# Burn rate fires on how fast the budget is being spent. Both windows are
# required, and the short one must be at least twice the SLO's slice interval.
resource "openobserve_alert" "burning_fast" {
  name         = "checkout-burning-fast"
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.pagerduty.name]

  query_condition {
    type = "slo"

    slo_condition {
      slo_id            = openobserve_slo.checkout_availability.slo_id
      kind              = "burn_rate"
      operator          = ">"
      critical          = 14.4 # burns a 30-day budget in about two days
      long_window_secs  = 3600
      short_window_secs = 900
    }
  }

  trigger_condition {
    period    = 5
    frequency = 5
  }
}
```

### Composite alerts

```terraform
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Reliability"
}

# The children. Any scheduled or SLO alert can be a composite child; a
# real-time alert cannot, because it has no scheduled state to read.
resource "openobserve_alert" "error_rate" {
  name         = "checkout-error-rate"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = "app_logs"
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE service = 'checkout' AND status >= 500"
  }

  trigger_condition {
    period    = 10
    operator  = ">="
    threshold = 50
    frequency = 5
  }
}

resource "openobserve_alert" "recent_deploy" {
  name         = "checkout-recent-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = "app_logs"
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE event = 'deploy' AND service = 'checkout'"
  }

  trigger_condition {
    period    = 30
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# "Errors, and a deploy went out recently" is a different, far more actionable
# page than either alert on its own. A composite says exactly that without
# duplicating a query or adding a second evaluation cost: it reads the states
# its children already computed.
resource "openobserve_composite_alert" "bad_deploy" {
  name         = "checkout-bad-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.slack.name]
  description  = "Checkout is erroring right after a deploy"

  expression = "{${openobserve_alert.error_rate.alert_id}} && {${openobserve_alert.recent_deploy.alert_id}}"

  silence = 30
}

# Referencing a child's alert_id is also what tells Terraform the composite
# depends on it, so destroys happen in the right order. The server refuses to
# delete an alert while a composite still names it.

# A fuller example. `!` inverts a child, which is how you express "the thing
# fired and the safeguard did not".
resource "openobserve_composite_alert" "unmitigated_errors" {
  name         = "checkout-unmitigated-errors"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.slack.name]

  expression = "{${openobserve_alert.error_rate.alert_id}} && !{${openobserve_alert.recent_deploy.alert_id}}"

  # Only react to children at critical; a child sitting at warning does not
  # count as firing here.
  warning_counts_as_firing = false

  # A child that stops reporting stops satisfying the expression, so a broken
  # child cannot hold this composite firing indefinitely.
  stale_child_policy = "treat_as_false"

  silence  = 60
  priority = 2
  tags     = ["prod", "service:checkout"]
}

# A composite may reference another composite, one level deep. Deeper nesting
# is rejected.
resource "openobserve_composite_alert" "checkout_unhealthy" {
  name         = "checkout-unhealthy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.slack.name]

  expression = "{${openobserve_composite_alert.bad_deploy.alert_id}} || {${openobserve_composite_alert.unmitigated_errors.alert_id}}"
}
```
