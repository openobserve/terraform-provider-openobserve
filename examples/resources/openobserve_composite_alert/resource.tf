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
