resource "openobserve_folder" "platform" {
  name = "Platform"
}

resource "openobserve_dashboard" "errors" {
  folder_id = openobserve_folder.platform.folder_id

  dashboard_json = jsonencode({
    version     = 5
    title       = "Service Health"
    description = "Errors per service over time"
    role        = ""
    owner       = "platform@example.com"

    tabs = [{
      tabId = "default"
      name  = "Default"
      panels = [
        {
          id          = "errors_over_time"
          type        = "line"
          title       = "Errors over time"
          description = ""
          config      = { show_legends = true }
          queryType   = "sql"
          layout      = { x = 0, y = 0, w = 24, h = 9, i = 1 }

          queries = [{
            query       = "SELECT histogram(_timestamp) AS x_axis_1, count(_timestamp) AS y_axis_1 FROM \"app_logs\" WHERE level = 'error' GROUP BY x_axis_1 ORDER BY x_axis_1"
            customQuery = true
            config      = { promql_legend = "" }

            fields = {
              # Watch the casing. Every key in a panel is camelCase — tabId,
              # queryType, customQuery, aggregationFunction — except this
              # `fields` object, which the API defines in snake_case.
              stream      = "app_logs"
              stream_type = "logs"

              # `alias` must match the SQL column alias: that is how a chart
              # binds an axis to a column in the result set.
              x = [{ label = "Timestamp", alias = "x_axis_1", column = "_timestamp" }]
              y = [{ label = "Count", alias = "y_axis_1", column = "_timestamp", aggregationFunction = "count" }]

              # Required, and it must be an object. An empty group is the
              # "no filter" case; a list-shaped value is rejected outright.
              filter = { filterType = "group", logicalOperator = "AND", conditions = [] }
            }
          }]
        },
      ]
    }]
  })
}

# Hand-writing panel JSON is tedious and easy to get subtly wrong. Building the
# dashboard in the UI and reading it back with the `openobserve_dashboard` data
# source gives you the exact schema for whichever chart types you use. Save that
# document to a file and apply it directly.
resource "openobserve_dashboard" "from_file" {
  folder_id      = openobserve_folder.platform.folder_id
  dashboard_json = file("${path.module}/dashboards/latency.json")
}
