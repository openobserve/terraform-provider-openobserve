data "openobserve_alert" "high_error_rate" {
  name = "high-error-rate"
}

output "alert_destinations" {
  value = data.openobserve_alert.high_error_rate.destinations
}
