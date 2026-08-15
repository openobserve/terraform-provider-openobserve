data "openobserve_alerts" "all" {}

# Alerts that are configured but not currently being evaluated.
output "disabled_alerts" {
  value = [for a in data.openobserve_alerts.all.alerts : a.name if !a.enabled]
}
