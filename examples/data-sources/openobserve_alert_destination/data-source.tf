data "openobserve_alert_destination" "existing_pagerduty" {
  name = "pagerduty"
}

resource "openobserve_alert" "example" {
  name         = "disk-pressure"
  stream_type  = "logs"
  stream_name  = "system"
  destinations = [data.openobserve_alert_destination.existing_pagerduty.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(*) AS total FROM \"system\" WHERE message LIKE '%disk pressure%'"
  }

  trigger_condition {
    period    = 10
    threshold = 1
  }
}
