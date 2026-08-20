# Installing the community alert library

`github.com/openobserve/o2-alerts-library` is a curated collection of alert
rules exported as raw API JSON, indexed by a `manifest.json` the maintainers
generate in CI.

Every field used by every alert in that library maps onto `openobserve_alert`,
so the whole library installs with no gaps. This was verified by applying all 87
alerts against a live server: idempotent, no drift.

## 1. Layout

```
o2-alerts-library/
├── manifest.json                       machine index, the thing to read
├── packs/
│   ├── k8s/alerts/<category>/<name>.json
│   └── openobserve/alerts/<category>/<name>.json
└── scripts/generate_manifest.py
```

A `manifest.json` entry:

```json
{
  "id": "k8s/go_gc_pause_high",
  "name": "go_gc_pause_high",
  "pack": "k8s",
  "category": "app-performance",
  "title": "Go Gc Pause High",
  "severity": "warning",
  "description": "Warning: Go GC average pause time exceeds 100ms...",
  "stream": "go_gc_duration_seconds_sum",
  "stream_type": "metrics",
  "query_type": "promql",
  "required_streams": ["go_gc_duration_seconds_sum"],
  "path": "packs/k8s/alerts/app-performance/go_gc_pause_high.json",
  "content_hash": "1c09e8f6ac33"
}
```

**Read the manifest, not the tree.** `severity` and `category` exist only in the
manifest; the alert files themselves do not carry them. Globbing the directory
loses that metadata and does not fail when the library moves a file.

## 2. Two things that will bite

**Streams must exist first.** Applying the library to a fresh organization fails
on every alert with `HTTP 404: Stream <name> not found`. In a live cluster the
OTel collector creates them on first ingest. Declare them from `required_streams`
and let `openobserve_stream` adopt whatever ingestion already made.

**`required_streams` is just `[stream_name]`.** It is not a real dependency
analysis, so treat it as a convenience rather than a guarantee.

## 3. A working installer

```hcl
locals {
  manifest = jsondecode(file("${var.library_path}/manifest.json"))

  selected = [
    for a in local.manifest.alerts : a
    if (length(var.packs) == 0 || contains(var.packs, a.pack))
    && (length(var.categories) == 0 || contains(var.categories, a.category))
    && (length(var.severities) == 0 || contains(var.severities, a.severity))
  ]

  # Keyed by the manifest's stable "<pack>/<name>" id, so renaming a category
  # does not destroy and recreate every alert in it.
  alerts = {
    for a in local.selected : a.id => merge(a, {
      body = jsondecode(file("${var.library_path}/${a.path}"))
    })
  }

  priority_of = { critical = 1, warning = 3, info = 5 }

  required_streams = {
    for s in distinct(flatten([
      for a in local.selected : [
        for name in distinct(concat(a.required_streams, [a.stream])) :
        { name = name, type = a.stream_type }
      ]
    ])) : "${s.type}/${s.name}" => s
  }
}

resource "openobserve_stream" "required" {
  for_each    = local.required_streams
  name        = each.value.name
  stream_type = each.value.type
}

resource "openobserve_alert" "library" {
  for_each   = local.alerts
  depends_on = [openobserve_stream.required]

  name      = each.value.body.name
  folder_id = openobserve_folder.library.folder_id

  stream_type = each.value.body.stream_type
  stream_name = each.value.body.stream_name
  description = each.value.body.description
  enabled     = each.value.body.enabled

  # The exported files name whichever destination the authors used. Point every
  # installed alert at the destination this configuration owns instead.
  destinations = [openobserve_alert_destination.library.name]

  row_template      = each.value.body.row_template
  row_template_type = each.value.body.row_template_type
  is_real_time      = each.value.body.is_real_time
  tz_offset         = try(each.value.body.tz_offset, 0)

  # Severity lives only in the manifest. Carrying it onto the alert makes it
  # queryable in OpenObserve itself.
  priority = local.priority_of[each.value.severity]
  tags     = [each.value.pack, each.value.category, each.value.severity]

  query_condition {
    type   = each.value.body.query_condition.type
    sql    = try(each.value.body.query_condition.sql, null)
    promql = try(each.value.body.query_condition.promql, null)

    dynamic "promql_condition" {
      for_each = try(each.value.body.query_condition.promql_condition, null) == null ? [] : [each.value.body.query_condition.promql_condition]
      content {
        column   = promql_condition.value.column
        operator = promql_condition.value.operator
        # The provider re-encodes a numeric-looking string as a JSON number, so
        # a threshold round-trips whether the export wrote 1 or 1.5.
        value       = tostring(promql_condition.value.value)
        ignore_case = try(promql_condition.value.ignore_case, null)
      }
    }
  }

  trigger_condition {
    period            = each.value.body.trigger_condition.period
    operator          = each.value.body.trigger_condition.operator
    threshold         = each.value.body.trigger_condition.threshold
    frequency         = each.value.body.trigger_condition.frequency
    frequency_type    = each.value.body.trigger_condition.frequency_type
    cron              = try(each.value.body.trigger_condition.cron, "")
    silence           = each.value.body.trigger_condition.silence
    timezone          = try(each.value.body.trigger_condition.timezone, null)
    tolerance_in_secs = try(each.value.body.trigger_condition.tolerance_in_secs, null)
    align_time        = try(each.value.body.trigger_condition.align_time, true)
  }
}
```

The complete runnable version, with the template, destination and folder, is in
`examples/alert-library/`.

## 4. Why `dynamic` is needed

`query_condition`, `trigger_condition` and `promql_condition` are **blocks**, not
attributes, so they cannot be assigned from a variable. A block that is present
for some elements of a `for_each` and absent for others needs `dynamic` with a
zero-or-one element list, which is what the `promql_condition` block above does.

This pattern applies to any set of exported alert JSON, not just this library.

## 5. What the JSON format cannot express

Worth raising with anyone installing the library, because these are improvements
the export format has no way to carry.

**Composite alerts.** The library has no way to say "page when A and B". Layer
them on top by looking children out of the `for_each` map:

```hcl
resource "openobserve_composite_alert" "unexplained_crashloop" {
  name       = "k8s_unexplained_crashloop"
  folder_id  = openobserve_folder.library.folder_id
  expression = "{${openobserve_alert.library["k8s/pod_crashloop_backoff"].alert_id}} && !{${openobserve_alert.library["k8s/pod_image_pull_backoff"].alert_id}}"
}
```

**Two-level alerts.** Some packs ship a "high" alert at 80 and a "critical"
alert at 90 with identical queries, so both fire at 91 and the same condition
pages twice. That is one alert with `promql_warning_value`; see `alerts.md`.

**Per-group alerting.** SQL alerts of the form `GROUP BY ... HAVING` collapse to
one verdict, so twelve breaching namespaces produce one notification. Rewritten
as an aggregation with `multi_alert = true`, each pages about itself.

Prefer fixing these in the library over papering over them in an installer: an
installer that silently merges two alerts a user asked for is doing the wrong
favour.

## 6. Version pinning

`manifest.json` carries `format_version`. Assert it, so a format change fails
loudly instead of producing a half-populated plan:

```hcl
locals {
  manifest = jsondecode(file("${var.library_path}/manifest.json"))
}

check "manifest_format" {
  assert {
    condition     = startswith(local.manifest.format_version, "1.")
    error_message = "Alert library manifest format ${local.manifest.format_version} is newer than this configuration understands."
  }
}
```
