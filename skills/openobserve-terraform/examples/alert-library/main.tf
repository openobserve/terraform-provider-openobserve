# Install the community alert library from a local checkout of
# github.com/openobserve/o2-alerts-library.
#
#   terraform apply -parallelism=1 -var library_path=/path/to/o2-alerts-library
#
# Credentials come from the environment:
#   OPENOBSERVE_ENDPOINT, OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID

terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.3"
    }
  }
}

provider "openobserve" {}

variable "library_path" {
  description = "Path to a checkout of openobserve/o2-alerts-library."
  type        = string
}

variable "packs" {
  description = "Packs to install. Empty installs every pack."
  type        = list(string)
  default     = []
}

variable "categories" {
  description = "Categories to install. Empty installs every category."
  type        = list(string)
  default     = []
}

variable "severities" {
  description = "Severities to install: critical, warning, info. Empty installs every severity."
  type        = list(string)
  default     = []
}

variable "create_missing_streams" {
  description = "Create the streams the selected alerts query, so alerts can be applied before any data has been ingested."
  type        = bool
  default     = true
}

variable "webhook_url" {
  type      = string
  default   = "https://example.com/hook"
  sensitive = true
}

# manifest.json is the library's own machine index. Reading it rather than
# globbing the tree means the selection below sees the curated metadata
# (severity, category, required_streams) that the alert files do not carry, and
# it fails loudly if the library moves a file.
locals {
  manifest = jsondecode(file("${var.library_path}/manifest.json"))

  selected = [
    for a in local.manifest.alerts : a
    if(length(var.packs) == 0 || contains(var.packs, a.pack))
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

  # The library records severity only in the manifest. Carrying it onto the
  # alert makes it queryable in OpenObserve itself.
  priority_of = {
    critical = 1
    warning  = 3
    info     = 5
  }

  # The server refuses to create an alert against a stream that does not exist
  # yet, with a bare HTTP 404. In a real cluster the OTel collector creates
  # these on first ingest, so this only bites when alerts are applied before any
  # data has arrived. openobserve_stream adopts a stream that ingestion already
  # created rather than fighting over it.
  required_streams = {
    for s in distinct(flatten([
      for a in local.selected : [
        for name in distinct(concat(a.required_streams, [a.stream])) :
        { name = name, type = a.stream_type }
      ]
    ])) : "${s.type}/${s.name}" => s
  }
}

# Fail loudly rather than half-populating a plan if the library's index format
# moves on.
check "manifest_format" {
  assert {
    condition     = startswith(local.manifest.format_version, "1.")
    error_message = "Alert library manifest format ${local.manifest.format_version} is newer than this configuration understands."
  }
}

resource "openobserve_stream" "required" {
  for_each = var.create_missing_streams ? local.required_streams : {}

  name        = each.value.name
  stream_type = each.value.type
}

resource "openobserve_alert_template" "library" {
  name = "alert_library_template"
  type = "http"
  body = jsonencode({
    text = "*Alert:* {alert_name}\n{rows}"
  })
}

resource "openobserve_alert_destination" "library" {
  name     = "alert_library_dest"
  type     = "http"
  url      = var.webhook_url
  method   = "post"
  template = openobserve_alert_template.library.name
}

resource "openobserve_folder" "library" {
  folder_type = "alerts"
  name        = "Alert Library"
}

resource "openobserve_alert" "library" {
  for_each = local.alerts

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

  priority = local.priority_of[each.value.severity]
  tags     = [each.value.pack, each.value.category, each.value.severity]

  # query_condition is a BLOCK, not an attribute, so it cannot be assigned from
  # a variable. A nested block that is present for some elements and absent for
  # others needs dynamic with a zero-or-one element list.
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

output "installed_count" {
  value = length(openobserve_alert.library)
}

output "by_severity" {
  value = {
    for sev in distinct([for a in local.selected : a.severity]) :
    sev => length([for a in local.selected : a if a.severity == sev])
  }
}

output "required_streams" {
  description = "Every stream the selected alerts query. Nothing fires until these exist."
  value       = sort(keys(local.required_streams))
}
