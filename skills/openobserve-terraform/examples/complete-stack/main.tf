# A complete OpenObserve stack: stream, notification path, dashboard,
# objective, five kinds of alert, and a pipeline transforming records on the
# way in.
#
# Credentials come from the environment:
#   OPENOBSERVE_ENDPOINT, OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID

terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.3"
    }
  }
}

provider "openobserve" {}

variable "slack_webhook_url" {
  type      = string
  default   = "https://example.com/hook"
  sensitive = true
}

# --- Foundation --------------------------------------------------------------

resource "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs"

  data_retention        = 30
  full_text_search_keys = ["message"]
  index_fields          = ["level"]

  # A partition key cannot also be a secondary index field, so these two lists
  # stay disjoint.
  partition_keys = [
    { field = "service", type = "value" },
  ]
}

resource "openobserve_folder" "platform" {
  folder_type = "dashboards"
  name        = "Platform"
}

# Alerts and SLOs share this one. There is no SLO folder type.
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Reliability"
}

# --- Notification ------------------------------------------------------------

resource "openobserve_alert_template" "slack" {
  name = "slack_platform"
  type = "http"
  body = jsonencode({
    text = "{alert_name} fired on {stream_name} at {alert_start_time}\n{rows}\n{alert_url}"
  })
}

resource "openobserve_alert_destination" "slack" {
  name     = "slack-platform"
  type     = "http"
  url      = var.slack_webhook_url
  method   = "post"
  template = openobserve_alert_template.slack.name
}

# --- Dashboard ---------------------------------------------------------------

resource "openobserve_dashboard" "service_health" {
  folder_id = openobserve_folder.platform.folder_id

  dashboard_json = jsonencode({
    version     = 5
    title       = "Service Health"
    description = "Errors per service over time"
    role        = ""
    owner       = "platform@example.com"

    tabs = [{
      tabId = "default"
      name  = "Default"
      panels = [{
        id          = "errors_over_time"
        type        = "line"
        title       = "Errors over time"
        description = ""
        config      = { show_legends = true }
        queryType   = "sql"
        layout      = { x = 0, y = 0, w = 24, h = 9, i = 1 }

        queries = [{
          query       = "SELECT histogram(_timestamp) AS x_axis_1, count(_timestamp) AS y_axis_1 FROM \"app_logs\" WHERE level = 'error' GROUP BY x_axis_1 ORDER BY x_axis_1"
          customQuery = true
          config      = { promql_legend = "" }

          fields = {
            # Everything in a panel is camelCase except this object, which the
            # API defines in snake_case.
            stream      = openobserve_stream.app_logs.name
            stream_type = "logs"

            # alias must match the SQL column alias; that is how a chart binds
            # an axis to a column.
            x = [{ label = "Timestamp", alias = "x_axis_1", column = "_timestamp" }]
            y = [{ label = "Count", alias = "y_axis_1", column = "_timestamp", aggregationFunction = "count" }]

            # Required, and must be an object. A list-shaped value is rejected.
            filter = { filterType = "group", logicalOperator = "AND", conditions = [] }
          }
        }]
      }]
    }]
  })
}

# --- Objective ---------------------------------------------------------------

resource "openobserve_slo" "checkout_availability" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "checkout_availability"

  target              = 99.9
  window_secs         = 2592000 # only 7d, 30d or 90d are accepted
  slice_interval_secs = 300

  count_sli {
    single_query {
      stream      = openobserve_stream.app_logs.name
      stream_type = "logs"
      scope       = "service = 'checkout'"
      good_expr   = "status < 500"
    }
  }
}

# --- Alerts ------------------------------------------------------------------

# 1. Threshold alert: one query, one verdict.
resource "openobserve_alert" "high_error_rate" {
  name         = "high-error-rate"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]
  description  = "Error volume above the acceptable rate"

  priority = 2
  tags     = ["prod", "service:checkout"]

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
}

# 2. Per-group alert: one page per slow service, not one page saying
#    "something is slow".
resource "openobserve_alert" "slow_services" {
  name         = "slow-services"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "custom"

    aggregation {
      group_by    = ["service"]
      function    = "p95"
      multi_alert = true

      having {
        column   = "duration_ms"
        operator = ">"
        # An aggregation's having.value must be numeric.
        value = "1000"
      }

      # Warning goes here, not on trigger_condition, which is the count gate.
      warning_value = 500
    }
  }

  trigger_condition {
    period    = 10
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# 3. A deploy marker, used below as a composite child.
resource "openobserve_alert" "recent_deploy" {
  name         = "recent-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE event = 'deploy'"
  }

  trigger_condition {
    period    = 30
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# 4. Budget alert on the objective. No threshold or operator: an SLO alert has
#    no count gate, and the server rejects them rather than ignoring them.
resource "openobserve_alert" "budget_burned" {
  name         = "checkout-budget-burned"
  folder_id    = openobserve_folder.reliability.folder_id
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]

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

  trigger_condition {
    period    = 5
    frequency = 5
  }
}

# 5. Composite: errors are only worth paging on when a deploy explains them.
#    Interpolating each child's alert_id is what tells Terraform about the
#    dependency, which is also what makes destroy ordering correct.
resource "openobserve_composite_alert" "bad_deploy" {
  name         = "checkout-bad-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.slack.name]
  description  = "Checkout is erroring right after a deploy"

  expression = "{${openobserve_alert.high_error_rate.alert_id}} && {${openobserve_alert.recent_deploy.alert_id}}"

  silence  = 30
  priority = 1
  tags     = ["prod", "composite"]
}

# --- Pipeline ----------------------------------------------------------------
#
# Records are transformed on the way in, before anything above ever queries
# them. `function_name` references the function resource rather than naming it:
# that reference is what orders creation and teardown, and the server refuses to
# delete a function a pipeline still uses.

resource "openobserve_stream" "app_logs_clean" {
  name        = "app_logs_clean"
  stream_type = "logs"
}

resource "openobserve_function" "redact_email" {
  name = "redact_email"

  function = <<-VRL
    .email = "[redacted]"
  VRL
}

resource "openobserve_pipeline" "redact_pii" {
  name        = "redact_pii"
  description = "Strip email addresses before they land"
  stream_name = openobserve_stream.app_logs.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.app_logs.name
  }

  node {
    id            = "redact"
    type          = "function"
    function_name = openobserve_function.redact_email.name
  }

  node {
    id          = "out"
    type        = "stream"
    stream_name = openobserve_stream.app_logs_clean.name
  }

  # Edges are multi-line: HCL allows only one argument on a single-line block.
  edge {
    from = "in"
    to   = "redact"
  }

  edge {
    from = "redact"
    to   = "out"
  }
}

# --- Reading it back ---------------------------------------------------------

data "openobserve_slo" "checkout" {
  name       = openobserve_slo.checkout_availability.name
  depends_on = [openobserve_slo.checkout_availability]
}

output "budget_remaining_pct" {
  description = "Null until the first evaluation pass has run."
  value       = try(data.openobserve_slo.checkout.status.error_budget_remaining, null)
}

output "objective_frozen" {
  description = "True when coverage is below the floor: neither healthy nor breached."
  value       = try(data.openobserve_slo.checkout.status.no_data, null)
}

output "composite_children" {
  value = openobserve_composite_alert.bad_deploy.child_alert_ids
}

# io_type is inferred from the edges rather than written by hand.
output "pipeline_io_types" {
  value = { for n in openobserve_pipeline.redact_pii.node : n.id => n.io_type }
}
