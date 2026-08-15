data "openobserve_alert_destinations" "all" {}

output "email_destinations" {
  value = [for d in data.openobserve_alert_destinations.all.destinations : d.name if d.type == "email"]
}
