# Every alert family the provider supports, side by side, so the differences
# are visible in one file.
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
  name        = "families_app_logs"
  stream_type = "logs"
}

resource "openobserve_stream" "app_metrics" {
  name        = "families_app_metrics"
  stream_type = "metrics"
}

resource "openobserve_folder" "families" {
  folder_type = "alerts"
  name        = "Alert Families"
}

resource "openobserve_alert_template" "webhook" {
  name = "families_template"
  type = "http"
  body = jsonencode({ text = "{alert_name}\n{rows}" })
}

resource "openobserve_alert_destination" "webhook" {
  name     = "families_dest"
  type     = "http"
  url      = "https://example.com/hook"
  method   = "post"
  template = openobserve_alert_template.webhook.name
}

locals {
  common = {
    folder_id    = openobserve_folder.families.folder_id
    destinations = [openobserve_alert_destination.webhook.name]
  }
}

# --- 1. SQL: an aggregate compared against a threshold -----------------------

resource "openobserve_alert" "sql_threshold" {
  name         = "families-sql-threshold"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"families_app_logs\" WHERE level = 'error'"
  }

  trigger_condition {
    period    = 15
    operator  = ">="
    threshold = 100
    frequency = 5
    silence   = 60
  }
}

# --- 2. Custom: UI-style conditions ------------------------------------------

resource "openobserve_alert" "custom_conditions" {
  name         = "families-custom-conditions"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name

  query_condition {
    type = "custom"
    conditions = jsonencode({
      or = [
        { column = "status", operator = "=", value = "failed", ignore_case = true },
      ]
    })
  }

  trigger_condition {
    period    = 10
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# --- 3. Aggregation, per group -----------------------------------------------
#
# The group is the group_by column set. Note where the warning level lives:
# aggregation.warning_value, NOT trigger_condition.warning_threshold, which is
# rejected on this family.

resource "openobserve_alert" "aggregation_multi" {
  name         = "families-aggregation-multi"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name

  query_condition {
    type = "custom"

    aggregation {
      group_by    = ["service"]
      function    = "p95"
      multi_alert = true

      having {
        column   = "duration_ms"
        operator = ">"
        value    = "1000" # must be numeric for an aggregation
      }

      warning_value = 500
    }
  }

  trigger_condition {
    period    = 10
    operator  = ">="
    threshold = 1 # the count gate: how many groups must breach
    frequency = 5
  }
}

# --- 4. PromQL, single verdict -----------------------------------------------

resource "openobserve_alert" "promql_simple" {
  name         = "families-promql-simple"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "metrics"
  stream_name  = openobserve_stream.app_metrics.name

  query_condition {
    type   = "promql"
    promql = "avg(rate(http_requests_total[5m]))"

    promql_condition {
      column   = "value"
      operator = ">"
      value    = "100"
    }

    promql_warning_value = 50
  }

  trigger_condition {
    period    = 5
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# --- 5. PromQL, per series ---------------------------------------------------
#
# The group is each returned series' label set, chosen by the expression's own
# by(...) clause. There is no separate label picker.

resource "openobserve_alert" "promql_multi" {
  name         = "families-promql-multi"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "metrics"
  stream_name  = openobserve_stream.app_metrics.name

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

  trigger_condition {
    period    = 5
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# --- 6. Cron scheduling ------------------------------------------------------

resource "openobserve_alert" "cron_scheduled" {
  name         = "families-cron"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"families_app_logs\""
  }

  trigger_condition {
    frequency_type = "cron"
    # SIX fields, seconds first: sec min hour day-of-month month day-of-week.
    # A five-field cron is rejected, and confusingly so: the server replaces a
    # LEADING "*" with the current second, so "*/10 * * * *" is rewritten to
    # "4 /10 * * * *" and then fails to parse.
    cron      = "0 */10 * * * *"
    timezone  = "UTC"
    period    = 10
    operator  = ">="
    threshold = 1
    silence   = 30
  }
}

# --- 7. Deduplication --------------------------------------------------------

resource "openobserve_alert" "deduplicated" {
  name         = "families-deduplicated"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total, service FROM \"families_app_logs\" WHERE level = 'error' GROUP BY service"
  }

  trigger_condition {
    period    = 10
    operator  = ">="
    threshold = 1
    frequency = 5
  }

  deduplication {
    enabled             = true
    fingerprint_fields  = ["service"]
    time_window_minutes = 30
  }
}

# --- 8. Real-time ------------------------------------------------------------
#
# Evaluates as matching data arrives rather than on a schedule. Note that a
# real-time alert cannot be a composite child: it produces no scheduled state.

resource "openobserve_alert" "realtime" {
  name         = "families-realtime"
  folder_id    = local.common.folder_id
  destinations = local.common.destinations
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  is_real_time = true

  query_condition {
    type = "custom"
    conditions = jsonencode({
      or = [
        { column = "level", operator = "=", value = "fatal", ignore_case = false },
      ]
    })
  }

  trigger_condition {
    period    = 1
    operator  = ">="
    threshold = 1
    frequency = 1
    silence   = 10
  }
}

output "alert_ids" {
  value = {
    sql_threshold     = openobserve_alert.sql_threshold.alert_id
    custom_conditions = openobserve_alert.custom_conditions.alert_id
    aggregation_multi = openobserve_alert.aggregation_multi.alert_id
    promql_simple     = openobserve_alert.promql_simple.alert_id
    promql_multi      = openobserve_alert.promql_multi.alert_id
    cron_scheduled    = openobserve_alert.cron_scheduled.alert_id
    deduplicated      = openobserve_alert.deduplicated.alert_id
    realtime          = openobserve_alert.realtime.alert_id
  }
}
