# Service level objectives: all three indicator types, and both kinds of alert
# on top of them.
#
# Credentials come from the environment:
#   OPENOBSERVE_ENDPOINT, OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID

terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.4"
    }
  }
}

provider "openobserve" {}

resource "openobserve_stream" "app_logs" {
  name        = "slo_example_logs"
  stream_type = "logs"
}

resource "openobserve_stream" "app_metrics" {
  name        = "slo_example_metrics"
  stream_type = "metrics"
}

# SLOs live in ALERT folders. There is no SLO folder type.
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "SLO Examples"
}

resource "openobserve_alert_template" "webhook" {
  name = "slo_example_template"
  type = "http"
  body = jsonencode({ text = "{alert_name}\n{rows}" })
}

resource "openobserve_alert_destination" "webhook" {
  name     = "slo_example_dest"
  type     = "http"
  url      = "https://example.com/hook"
  method   = "post"
  template = openobserve_alert_template.webhook.name
}

# --- 1. count_sli / single_query ---------------------------------------------
#
# The default choice for availability. Numerator and denominator are counted in
# one scan, so they are provably from the same rows.

resource "openobserve_slo" "availability" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "slo_example_availability"

  target              = 99.9    # strictly inside (0, 100)
  window_secs         = 2592000 # only 604800, 2592000 or 7776000
  slice_interval_secs = 300

  count_sli {
    single_query {
      stream      = openobserve_stream.app_logs.name
      stream_type = "logs"
      scope       = "service = 'checkout'" # denominator filter
      good_expr   = "status < 500"         # numerator predicate
    }
  }
}

# --- 2. count_sli / promql ---------------------------------------------------
#
# For pre-aggregated counters, where "good" only exists as arithmetic between
# series. The range selector should equal the slice interval, because the
# evaluator samples at slice ends.

resource "openobserve_slo" "availability_from_counters" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "slo_example_counters"

  target              = 99.5
  window_secs         = 604800
  slice_interval_secs = 300

  count_sli {
    promql {
      good  = "sum(increase(http_requests_total{status!~\"5..\"}[5m]))"
      total = "sum(increase(http_requests_total[5m]))"
    }
  }
}

# --- 3. count_sli / dual_query -----------------------------------------------
#
# Weaker: two scans that cannot be proven to have seen the same instant. It
# exists for imported definitions that cannot be folded into one query.
#
# Each query must project slice_start, every group_by column, and exactly one
# numeric zo_slo_value. histogram() needs a QUOTED interval literal.

resource "openobserve_slo" "availability_dual" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "slo_example_dual"

  target              = 99.0
  window_secs         = 604800
  slice_interval_secs = 300

  count_sli {
    dual_query {
      good {
        stream      = openobserve_stream.app_logs.name
        stream_type = "logs"
        sql         = "SELECT histogram(_timestamp, '5 minute') AS slice_start, count(*) AS zo_slo_value FROM \"slo_example_logs\" WHERE status < 500 GROUP BY slice_start"
      }
      total {
        stream      = openobserve_stream.app_logs.name
        stream_type = "logs"
        sql         = "SELECT histogram(_timestamp, '5 minute') AS slice_start, count(*) AS zo_slo_value FROM \"slo_example_logs\" GROUP BY slice_start"
      }
    }
  }
}

# --- 4. time_slice_sli -------------------------------------------------------
#
# The natural choice for latency: whether a percentile stayed under a bound,
# rather than counting individual events. Only orderable comparators, because a
# slice with no value is a gap rather than a failure.

resource "openobserve_slo" "latency" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "slo_example_latency"

  target              = 99.0
  window_secs         = 604800
  slice_interval_secs = 300

  time_slice_sli {
    stream         = openobserve_stream.app_logs.name
    stream_type    = "logs"
    query_language = "sql"
    query          = "SELECT approx_percentile_cont(duration_ms, 0.95) AS zo_slo_value FROM \"slo_example_logs\""
    comparator     = "<"
    threshold      = 300
  }
}

# --- 5. time_slice_sli with absent_is_bad ------------------------------------
#
# For freshness, where a silent pipeline IS the failure. Ungrouped only: gap
# fill cannot see a group missing from an entire pass, so a grouped freshness
# objective would freeze instead of firing.

resource "openobserve_slo" "freshness" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "slo_example_freshness"

  target              = 99.0
  window_secs         = 604800
  slice_interval_secs = 300

  time_slice_sli {
    stream         = openobserve_stream.app_logs.name
    stream_type    = "logs"
    query_language = "sql"
    query          = "SELECT count(*) AS zo_slo_value FROM \"slo_example_logs\""
    comparator     = ">"
    threshold      = 0
    absent_is_bad  = true
  }
}

# --- 6. Grouped objective ----------------------------------------------------
#
# group_by lives on the objective and nowhere else: it is the single source of
# truth for slice identity, and every alert on it inherits these groups.

resource "openobserve_slo" "by_region" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "slo_example_by_region"

  target              = 99.9
  window_secs         = 2592000
  slice_interval_secs = 300
  group_by            = ["region"]

  count_sli {
    single_query {
      stream      = openobserve_stream.app_logs.name
      stream_type = "logs"
      good_expr   = "status < 500"
    }
  }
}

# --- 7. Error budget alert ---------------------------------------------------
#
# No threshold and no operator on trigger_condition: an SLO alert has no count
# gate, and the server rejects them rather than ignoring them.

resource "openobserve_alert" "budget_burned" {
  name         = "slo-example-budget-burned"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.webhook.name]

  query_condition {
    type = "slo"

    slo_condition {
      slo_id   = openobserve_slo.availability.slo_id
      kind     = "error_budget"
      operator = ">" # ascending only
      critical = 90  # 90% of the budget consumed
      warning  = 75
    }
  }

  trigger_condition {
    period    = 5
    frequency = 5
  }
}

# --- 8. Burn rate alert ------------------------------------------------------
#
# Both windows are required. The short one is deliberately not derived: its
# minimum is twice the objective's slice_interval_secs, so a guessed value can
# be rejected. 3600 / 12 = 300s fails against a 300s slice interval.

resource "openobserve_alert" "burning_fast" {
  name         = "slo-example-burning-fast"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.webhook.name]

  query_condition {
    type = "slo"

    slo_condition {
      slo_id            = openobserve_slo.availability.slo_id
      kind              = "burn_rate"
      operator          = ">"
      critical          = 14.4 # burns a 30-day budget in about two days
      long_window_secs  = 3600 # 1h to 48h, no longer than the SLO window
      short_window_secs = 900
    }
  }

  trigger_condition {
    period    = 5
    frequency = 5
  }
}

# --- Reading the measurement -------------------------------------------------

data "openobserve_slos" "all" {
  depends_on = [
    openobserve_slo.availability,
    openobserve_slo.latency,
    openobserve_slo.by_region,
  ]
}

output "budget_remaining_pct" {
  description = "Null until the first evaluation pass. Goes negative when overspent."
  value = {
    for s in data.openobserve_slos.all.slos :
    s.name => try(s.status.error_budget_remaining, null)
  }
}

output "frozen_objectives" {
  description = "Coverage below the floor: neither healthy nor breached, and not zero."
  value = [
    for s in data.openobserve_slos.all.slos :
    s.name if s.status != null && s.status.no_data
  ]
}
