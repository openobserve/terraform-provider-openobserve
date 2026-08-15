resource "openobserve_folder" "platform" {
  name = "Platform"
}

resource "openobserve_dashboard" "errors" {
  folder_id = openobserve_folder.platform.folder_id

  dashboard_json = jsonencode({
    version     = 5
    title       = "Error Rate"
    description = "Errors per service over time"
    role        = ""
    owner       = "platform@example.com"
    tabs = [{
      tabId = "default"
      name  = "Default"
      panels = [{
        id          = "errors_over_time"
        type        = "line"
        title       = "Errors over time"
        description = ""
        config = {
          show_legends = true
        }
        queryType = "sql"
        queries = [{
          query       = "SELECT histogram(_timestamp) AS x_axis_1, count(*) AS y_axis_1 FROM \"app_logs\" WHERE level = 'error' GROUP BY x_axis_1 ORDER BY x_axis_1"
          customQuery = true
          fields = {
            stream      = "app_logs"
            stream_type = "logs"
            x           = [{ label = "Timestamp", alias = "x_axis_1", column = "_timestamp" }]
            y           = [{ label = "Count", alias = "y_axis_1", column = "*", aggregationFunction = "count" }]
          }
        }]
        layout = { x = 0, y = 0, w = 24, h = 9, i = 1 }
      }]
    }]
  })
}

# A dashboard exported from the OpenObserve UI can be checked into the repo and
# applied directly.
resource "openobserve_dashboard" "from_file" {
  folder_id      = openobserve_folder.platform.folder_id
  dashboard_json = file("${path.module}/dashboards/latency.json")
}
