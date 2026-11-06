# Downtimes need OpenObserve Enterprise v1.2.0-rc1 or later.

# A one-time maintenance window: mute the checkout alerts during a database
# upgrade, and tell the on-call channel when it starts and ends.
resource "openobserve_downtime" "db_upgrade" {
  name        = "Checkout DB upgrade"
  reason      = "Postgres 16 upgrade, CHG-1234"
  show_banner = true

  # Only what fires for the checkout service.
  condition = jsonencode({
    type     = "pair"
    key      = "service"
    operator = "="
    value    = "checkout"
  })

  target {
    module = "alerts"
  }
  target {
    module = "anomaly_detections"
  }

  schedule {
    starts_at = "2026-12-05T22:00:00Z"
    ends_at   = "2026-12-06T01:00:00Z"
  }

  notifications {
    destinations = [openobserve_alert_destination.oncall.name]
    events       = ["started", "ending_soon", "ended"]
  }
}

# A recurring window: every Saturday and Sunday from 02:00 to 03:00 Berlin
# time, mute the synthetics checks tagged `batch`, and count the SLO's
# minutes in that window as good.
resource "openobserve_downtime" "weekend_batch" {
  name        = "Weekend batch jobs"
  show_banner = false

  target {
    module = "synthetics"
    tags   = ["batch"]
  }
  target {
    module   = "slos"
    ids      = [openobserve_slo.checkout_availability.slo_id]
    slo_mode = "count_as_good"
  }

  schedule {
    repeat           = "weekly"
    weekdays         = [6, 7]
    starts_at        = "2026-12-01T00:00:00Z"
    timezone         = "Europe/Berlin"
    start_time_local = "02:00"
    duration_secs    = 3600
  }
}
