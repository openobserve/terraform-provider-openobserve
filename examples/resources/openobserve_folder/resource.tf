resource "openobserve_folder" "platform_dashboards" {
  name        = "Platform"
  description = "Dashboards owned by the platform team"
}

resource "openobserve_folder" "platform_alerts" {
  folder_type = "alerts"
  name        = "Platform"
  description = "Alerts owned by the platform team"
}
