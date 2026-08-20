# Composite alerts: firing on a boolean expression over other alerts.
#
# Credentials come from the environment:
#   OPENOBSERVE_ENDPOINT, OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID

terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.2"
    }
  }
}

provider "openobserve" {}

resource "openobserve_stream" "app_logs" {
  name        = "comp_example_logs"
  stream_type = "logs"
}

# A composite requires its folder to already exist. Unlike an ordinary alert, it
# will not create the default folder on demand.
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Composite Examples"
}

resource "openobserve_alert_template" "webhook" {
  name = "comp_example_template"
  type = "http"
  body = jsonencode({ text = "{alert_name}\n{rows}" })
}

resource "openobserve_alert_destination" "webhook" {
  name     = "comp_example_dest"
  type     = "http"
  url      = "https://example.com/hook"
  method   = "post"
  template = openobserve_alert_template.webhook.name
}

# --- Children ----------------------------------------------------------------
#
# Any scheduled or SLO alert can be a composite child. A real-time alert cannot:
# it has no scheduled state for a composite to read.

locals {
  child_defaults = {
    folder_id    = openobserve_folder.reliability.folder_id
    destinations = [openobserve_alert_destination.webhook.name]
    stream_type  = "logs"
    stream_name  = openobserve_stream.app_logs.name
  }
}

resource "openobserve_alert" "error_rate" {
  name         = "comp-error-rate"
  folder_id    = local.child_defaults.folder_id
  destinations = local.child_defaults.destinations
  stream_type  = local.child_defaults.stream_type
  stream_name  = local.child_defaults.stream_name

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"comp_example_logs\" WHERE status >= 500"
  }

  trigger_condition {
    period    = 10
    operator  = ">="
    threshold = 50
    frequency = 5
  }
}

resource "openobserve_alert" "recent_deploy" {
  name         = "comp-recent-deploy"
  folder_id    = local.child_defaults.folder_id
  destinations = local.child_defaults.destinations
  stream_type  = local.child_defaults.stream_type
  stream_name  = local.child_defaults.stream_name

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"comp_example_logs\" WHERE event = 'deploy'"
  }

  trigger_condition {
    period    = 30
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

resource "openobserve_alert" "maintenance_window" {
  name         = "comp-maintenance-window"
  folder_id    = local.child_defaults.folder_id
  destinations = local.child_defaults.destinations
  stream_type  = local.child_defaults.stream_type
  stream_name  = local.child_defaults.stream_name

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"comp_example_logs\" WHERE event = 'maintenance_start'"
  }

  trigger_condition {
    period    = 60
    operator  = ">="
    threshold = 1
    frequency = 5
  }
}

# --- 1. AND: two facts that are only actionable together ---------------------
#
# The expression is deliberately written without the outer parentheses the
# server canonicalizes to. The provider compares expressions rather than
# strings, so this spelling is preserved and never shows as drift.

resource "openobserve_composite_alert" "bad_deploy" {
  name         = "comp-bad-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.webhook.name]
  description  = "Errors right after a deploy"

  expression = "{${openobserve_alert.error_rate.alert_id}} && {${openobserve_alert.recent_deploy.alert_id}}"

  silence  = 30
  priority = 1
  tags     = ["prod", "composite"]
}

# --- 2. NOT: suppress a signal that is already explained ---------------------

resource "openobserve_composite_alert" "unexplained_errors" {
  name         = "comp-unexplained-errors"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.webhook.name]
  description  = "Errors outside a maintenance window"

  expression = "{${openobserve_alert.error_rate.alert_id}} && !{${openobserve_alert.maintenance_window.alert_id}}"

  # Only react to children at critical; a child at warning does not count.
  warning_counts_as_firing = false

  # A child that stops reporting must not hold this composite firing forever.
  stale_child_policy = "treat_as_false"

  silence = 60
}

# --- 3. Nesting: a composite of composites, one level deep -------------------
#
# Depth 3 is rejected with composite_too_deep.

resource "openobserve_composite_alert" "checkout_unhealthy" {
  name         = "comp-checkout-unhealthy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.webhook.name]

  expression = "{${openobserve_composite_alert.bad_deploy.alert_id}} || {${openobserve_composite_alert.unexplained_errors.alert_id}}"

  # Absence of a heartbeat is itself the signal here, so a stale child should
  # keep satisfying the expression rather than quietly dropping out.
  stale_child_policy = "treat_as_true"
}

# --- Inspecting --------------------------------------------------------------

data "openobserve_composite_alert" "bad_deploy" {
  alert_id = openobserve_composite_alert.bad_deploy.alert_id
}

output "stored_expression" {
  description = "The fully parenthesized form the server actually persists."
  value       = data.openobserve_composite_alert.bad_deploy.expression
}

output "configured_expression" {
  description = "The spelling from configuration, preserved by the provider."
  value       = openobserve_composite_alert.bad_deploy.expression
}

output "children_currently_true" {
  value = [
    for c in data.openobserve_composite_alert.bad_deploy.children :
    c.name if c.truth
  ]
}

output "stale_children" {
  description = "A stale child contributes whatever stale_child_policy says, not its last value."
  value = [
    for c in data.openobserve_composite_alert.bad_deploy.children :
    c.name if c.stale
  ]
}

output "last_evaluation" {
  description = "Null until the composite has run once, which is not the same as false."
  value       = try(data.openobserve_composite_alert.bad_deploy.evaluation, null)
}

# Which composites hold error_rate as a child. This is what answers "why can I
# not destroy this alert".
data "openobserve_composite_alert_references" "error_rate" {
  alert_id = openobserve_alert.error_rate.alert_id

  # Without this the data source only depends on the child, so Terraform is free
  # to read it before the composites exist and it comes back empty. Any data
  # source that reports on relationships needs to depend on both ends.
  depends_on = [
    openobserve_composite_alert.bad_deploy,
    openobserve_composite_alert.unexplained_errors,
  ]
}

output "error_rate_blocked_by" {
  value = [
    for r in data.openobserve_composite_alert_references.error_rate.references :
    r.name
  ]
}

output "references_hidden_by_permissions" {
  description = "When not zero, an empty references list does not mean unreferenced."
  value       = data.openobserve_composite_alert_references.error_rate.hidden_reference_count
}
