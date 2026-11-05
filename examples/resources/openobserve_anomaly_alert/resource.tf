# Anomaly alerts need OpenObserve Enterprise v1.1.0 or later.
# They live in alert folders, like every other kind of alert.
resource "openobserve_folder" "anomalies" {
  folder_type = "alerts"
  name        = "Anomalies"
}

# Request volume per 5-minute bucket for one service. The band width is left
# unset (Auto), so training picks k, between 3 and 6, from the data.
resource "openobserve_anomaly_alert" "checkout_volume" {
  folder_id   = openobserve_folder.anomalies.folder_id
  name        = "checkout_volume"
  description = "Unusual checkout request volume"
  stream_name = "app_logs"

  filters = [
    { field = "service", operator = "=", value = "checkout" },
  ]
  detection_function       = "count"
  histogram_interval       = "5m"
  schedule_interval        = "15m"
  detection_window_seconds = 3600

  alert_destinations = [openobserve_alert_destination.slack.name]
}

# p95 latency per 5-minute bucket, with a fixed band and delivery settings:
# only a rise is delivered, and only once 3 of the last 4 buckets are out of
# band. It recovers when fewer than half of them are.
resource "openobserve_anomaly_alert" "checkout_latency" {
  folder_id   = openobserve_folder.anomalies.folder_id
  name        = "checkout_latency"
  stream_name = "app_logs"

  filters = [
    { field = "service", operator = "=", value = "checkout" },
  ]
  detection_function       = "p95"
  detection_function_field = "duration_ms"
  histogram_interval       = "5m"
  schedule_interval        = "15m"
  detection_window_seconds = 3600
  training_window_days     = 28

  band_width               = 4 # expected ± 4σ
  alert_direction          = "above"
  alert_window_buckets     = 4
  alert_window_fire_pct    = 75
  alert_window_recover_pct = 50

  alert_destinations = [openobserve_alert_destination.slack.name]
  priority           = 2
  tags               = ["prod", "team:checkout"]
}

# An error rate from custom SQL. The query computes the value of each bucket
# itself; detection_function_field names the output column that carries it.
# Let OpenObserve move k to deliver about two alerts a day instead of fixing
# it. alert_budget_per_day cannot be combined with band_width.
resource "openobserve_anomaly_alert" "error_rate" {
  folder_id   = openobserve_folder.anomalies.folder_id
  name        = "checkout_error_rate"
  stream_name = "app_logs"

  custom_sql = <<-SQL
    SELECT histogram(_timestamp, '1h') AS _timestamp,
           CAST(SUM(CASE WHEN status >= 500 THEN 1 ELSE 0 END) AS DOUBLE) / COUNT(*) AS value
    FROM "app_logs"
    WHERE service = 'checkout'
    GROUP BY _timestamp
  SQL

  detection_function       = "avg"
  detection_function_field = "value"
  histogram_interval       = "1h"
  schedule_interval        = "1h"
  detection_window_seconds = 7200

  alert_budget_per_day = 2

  alert_destinations = [openobserve_alert_destination.slack.name]
}

# Filled in once training has run; refresh to see them.
output "checkout_volume_band" {
  value = {
    grouping = openobserve_anomaly_alert.checkout_volume.band_grouping
    k        = openobserve_anomaly_alert.checkout_volume.band_k
    status   = openobserve_anomaly_alert.checkout_volume.status
  }
}
