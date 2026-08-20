# Dashboards

## 1. Why `dashboard_json`

OpenObserve has **eight** dashboard schema versions, `v1` through `v8`. The
provider takes the document whole rather than modelling attributes, because
modelling one version would break the other seven.

```hcl
resource "openobserve_dashboard" "errors" {
  folder_id      = openobserve_folder.platform.folder_id
  dashboard_json = jsonencode({ ... })
}
```

`GET` returns a versioned envelope, not the bare document:

```json
{
  "v5":   { "dashboardId": "...", "title": "...", "tabs": [...] },
  "version": 5,
  "hash": "653125742505097822",
  "updated_at": 1786750761112086
}
```

The provider reads the slot matching `version`, falling back to whichever slot is
populated so a future version still reads.

## 2. Structure

```
dashboard
└── tabs[]
    └── panels[]
        ├── config    { show_legends, ... }
        ├── layout    { x, y, w, h, i }   (optional in v8)
        └── queries[]
            ├── query        (SQL string)
            ├── customQuery  (bool)
            ├── config       { promql_legend }
            └── fields       { stream, stream_type, x[], y[], filter }
```

## 3. A verified working panel

This exact configuration was applied against a live server and the panel
confirmed stored.

```hcl
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
              stream      = "app_logs"
              stream_type = "logs"
              x = [{ label = "Timestamp", alias = "x_axis_1", column = "_timestamp" }]
              y = [{ label = "Count", alias = "y_axis_1", column = "_timestamp", aggregationFunction = "count" }]
              filter = { filterType = "group", logicalOperator = "AND", conditions = [] }
            }
          }]
        },
      ]
    }]
  })
}
```

## 4. Three traps

### Casing is inconsistent

Every key in a panel is camelCase (`tabId`, `queryType`, `customQuery`,
`aggregationFunction`, `filterType`) **except** the `fields` object, which the
server defines in snake_case: `stream_type`, not `streamType`.

This is visible in the Rust: `Tab`, `Panel`, `Query`, `AxisItem` and `Layout`
all carry `#[serde(rename_all = "camelCase")]`; `PanelFields` does not.

### `filter` is required and must be an object

A list-shaped value is rejected:

> `Failed to deserialize the JSON body into the target type: data did not match any variant of untagged enum PanelFilter`

The empty group is the "no filter" case:

```hcl
filter = { filterType = "group", logicalOperator = "AND", conditions = [] }
```

A real condition requires **all** of `type`, `values`, `column`,
`logicalOperator` and `filterType`:

```hcl
filter = {
  filterType      = "group"
  logicalOperator = "AND"
  conditions = [{
    type            = "list"
    values          = ["checkout"]
    column          = "service"
    operator        = null
    value           = null
    logicalOperator = "AND"
    filterType      = "condition"
  }]
}
```

### `alias` must match the SQL column alias

That is how a chart binds an axis to a column in the result set.
`count(_timestamp) AS y_axis_1` needs `alias = "y_axis_1"` on the y item. A
mismatch produces an empty chart rather than an error.

## 5. Updating

Three things must be right or the update misbehaves. The provider handles all
three; they matter when debugging a raw API call.

1. **`dashboardId` must be in the body.** Without it the server creates a
   *second* dashboard instead of updating. The provider injects it
   automatically.
2. **`hash` is a concurrency token**, passed as a query parameter and taken from
   the last read. The provider re-reads immediately before each write so a stale
   hash does not reject a legitimate update.
3. **`folder` is a query parameter** on both create and update.

## 6. Moving between folders

`PUT /api/{org}/folders/dashboards/{dashboard_id}` with `{"from": ..., "to": ...}`.
Changing `folder_id` triggers this automatically.

## 7. Finding which folder holds a dashboard

The list endpoint only searches the `default` folder when no folder is given, so
finding a dashboard's folder means enumerating every folder. The provider does
this on read and import.

## 8. The easier path

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

Save that document to a file and apply it directly:

```hcl
resource "openobserve_dashboard" "errors" {
  folder_id      = openobserve_folder.platform.folder_id
  dashboard_json = file("${path.module}/dashboards/service-health.json")
}
```

Drop `dashboardId` and `created` from the exported file; the server fills them
in.

## 9. Drift

The server enriches a stored dashboard with fields it manages (`dashboardId`,
`created`, and others). The provider uses **JSON subset** reconciliation: if the
server's document still contains everything the configured document asked for,
the configured value stays in state. Change a value the server disagrees with
and the difference surfaces normally.

This is why `jsonencode()` of a partial document is safe, and why adding a field
the server also sets does not fight.

## 10. Variables

```hcl
variables = {
  showDynamicFilters = true
  list = [{
    name  = "service"
    label = "Service"
    type  = "query_values"
    query_data = {
      stream      = "app_logs"
      stream_type = "logs"
      field       = "service"
      max_record_size = null
    }
    value        = ""
    multiSelect  = false
  }]
}
```

`type` is one of `query_values`, `constant`, `textbox`, `custom`. Only
`query_values` takes `query_data`.

## 11. Import

```bash
terraform import openobserve_dashboard.errors default/7123abc
```

```hcl
data "openobserve_dashboards" "all" {}

output "dashboard_ids" {
  value = { for d in data.openobserve_dashboards.all.dashboards : d.title => d.dashboard_id }
}
```
