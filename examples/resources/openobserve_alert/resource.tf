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
