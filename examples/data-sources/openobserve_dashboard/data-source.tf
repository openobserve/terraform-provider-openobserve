# Read a dashboard built in the UI so its document can be copied into Terraform.
data "openobserve_dashboard" "built_in_ui" {
  title = "Kubernetes Overview"
}

output "dashboard_document" {
  value = data.openobserve_dashboard.built_in_ui.dashboard_json
}
