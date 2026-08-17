---
page_title: "Dashboards"
subcategory: "Guides"
description: |-
  Panel JSON, the casing traps, and how to avoid hand-writing any of it.
---

# Dashboards

A dashboard is a JSON document. The provider takes it whole in `dashboard_json`
rather than modelling it attribute by attribute, because OpenObserve has eight
dashboard schema versions and modelling one of them would break the other seven.

## The shortcut: build it in the UI

Hand-writing panel JSON is tedious and easy to get subtly wrong. Build the
dashboard in the UI, then read it back:

```hcl
data "openobserve_dashboard" "built_in_ui" {
  title = "Service Health"
}

output "json" {
  value = data.openobserve_dashboard.built_in_ui.dashboard_json
}
```

Save that to a file and commit it:

```hcl
resource "openobserve_dashboard" "errors" {
  folder_id      = openobserve_folder.platform.folder_id
  dashboard_json = file("${path.module}/dashboards/service-health.json")
}
```

You get the exact schema for whichever chart types you use, and `jsonencode()`
is not fighting you. Drop `dashboardId` and `created` from the exported file if
present, since the server fills those in.

## Writing it by hand

The structure is a dashboard, containing tabs, containing panels:

```terraform
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
              # Watch the casing. Every key in a panel is camelCase (tabId,
              # queryType, customQuery, aggregationFunction) except this
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
```

## Three traps

**Casing is inconsistent.** Every key in a panel is camelCase (`tabId`,
`queryType`, `customQuery`, `aggregationFunction`, `filterType`) *except* the
`fields` object, which is snake_case: `stream_type`, not `streamType`. That is
how the server defines it.

**`filter` is required, and must be an object.** A list-shaped value is rejected
with `data did not match any variant of untagged enum PanelFilter`. An empty
group is the "no filter" case:

```hcl
filter = { filterType = "group", logicalOperator = "AND", conditions = [] }
```

**`alias` must match the SQL column alias.** That is how a chart binds an axis to
a column in the result set. If your query selects `count(_timestamp) AS
y_axis_1`, the y axis item needs `alias = "y_axis_1"`.

## Drift and server-managed fields

The server adds fields to the document it stores, such as `dashboardId`, `created`, and
others. Those additions are not drift, and the provider does not report them: as
long as the stored document still contains everything your configuration asked
for, your configuration is what stays in state. Change a value the server
disagrees with and the difference shows up normally.

## Folders

```hcl
resource "openobserve_folder" "platform" {
  name = "Platform" # folder_type defaults to "dashboards"
}
```

Changing `folder_id` on an existing dashboard moves it between folders.
