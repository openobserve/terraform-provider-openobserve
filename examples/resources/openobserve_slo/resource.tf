# SLOs live in alert folders — there is no separate SLO folder type.
resource "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Reliability"
}

# Availability: good requests over total requests, counted in a single scan.
# This is the form to prefer — one query means the numerator and denominator
# are provably drawn from the same rows.
resource "openobserve_slo" "checkout_availability" {
  folder_id   = openobserve_folder.reliability.folder_id
  name        = "checkout_availability"
  description = "Successful checkout requests over 30 days"

  target              = 99.9    # three nines
  window_secs         = 2592000 # 30 days
  slice_interval_secs = 300     # 5 minutes

  count_sli {
    single_query {
      stream      = "app_logs"
      stream_type = "logs"
      scope       = "service = 'checkout'"
      good_expr   = "status < 500"
    }
  }
}

# The same objective measured per region. Each group gets its own budget, and a
# multi-alert on this SLO pages once per breaching group rather than once overall.
resource "openobserve_slo" "checkout_availability_by_region" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "checkout_availability_by_region"

  target              = 99.5
  window_secs         = 2592000
  slice_interval_secs = 300
  group_by            = ["region"]
  tags                = ["prod", "team:checkout"]

  count_sli {
    single_query {
      stream      = "app_logs"
      stream_type = "logs"
      scope       = "service = 'checkout'"
      good_expr   = "status < 500"
    }
  }
}

# Latency: each 5-minute slice is good when p95 stays under 300ms. A slice the
# query could not answer is a gap, not a failure, which is why only orderable
# comparators are accepted here.
resource "openobserve_slo" "checkout_latency" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "checkout_latency"

  target              = 99.0
  window_secs         = 604800 # 7 days
  slice_interval_secs = 300

  time_slice_sli {
    stream         = "app_logs"
    stream_type    = "logs"
    query_language = "sql"
    query          = "SELECT approx_percentile_cont(duration_ms, 0.95) AS zo_slo_value FROM \"app_logs\" WHERE service = 'checkout'"
    comparator     = "<"
    threshold      = 300
  }
}

# Pipeline freshness. `absent_is_bad` flips the meaning of an empty slice: for
# this objective a silent pipeline is a broken pipeline, so absence is the
# failure signal rather than a gap. Ungrouped objectives only.
resource "openobserve_slo" "ingest_freshness" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "ingest_freshness"

  target              = 99.9
  window_secs         = 86400 # 1 day
  slice_interval_secs = 300

  time_slice_sli {
    stream         = "app_logs"
    stream_type    = "logs"
    query_language = "sql"
    query          = "SELECT count(_timestamp) AS zo_slo_value FROM \"app_logs\""
    comparator     = ">"
    threshold      = 0
    absent_is_bad  = true
  }
}

# Metrics-native counting. Pre-aggregated counters have no rows to classify —
# "good" only exists as arithmetic between series — so use a range selector
# equal to the slice interval.
resource "openobserve_slo" "http_success_rate" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "http_success_rate"

  target              = 99.9
  window_secs         = 2592000
  slice_interval_secs = 300

  count_sli {
    promql {
      good  = "sum(increase(http_requests_total{status!~\"5..\"}[5m]))"
      total = "sum(increase(http_requests_total[5m]))"
    }
  }
}

# Derive the objective from an alert: slices where the alert was firing are bad.
resource "openobserve_slo" "from_alert" {
  folder_id = openobserve_folder.reliability.folder_id
  name      = "api_error_budget"

  target              = 99.0
  window_secs         = 2592000
  slice_interval_secs = 300

  alert_sli {
    alert_id = openobserve_alert.high_error_rate.alert_id
  }
}
