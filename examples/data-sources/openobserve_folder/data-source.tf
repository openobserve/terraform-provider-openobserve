# Place a dashboard into a folder that already exists, without importing it.
data "openobserve_folder" "shared" {
  name = "Shared"
}

resource "openobserve_dashboard" "example" {
  folder_id      = data.openobserve_folder.shared.folder_id
  dashboard_json = file("${path.module}/dashboard.json")
}
