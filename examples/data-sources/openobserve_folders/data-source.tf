data "openobserve_folders" "dashboards" {
  folder_type = "dashboards"
}

output "folder_ids_by_name" {
  value = {
    for f in data.openobserve_folders.dashboards.folders : f.name => f.folder_id
  }
}
