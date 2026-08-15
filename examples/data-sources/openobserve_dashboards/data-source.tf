data "openobserve_dashboards" "platform" {
  folder_id = data.openobserve_folder.shared.folder_id
}

output "dashboard_titles" {
  value = [for d in data.openobserve_dashboards.platform.dashboards : d.title]
}
