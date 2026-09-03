# Streams and folders

Alerts, dashboards and SLOs all reference streams, so streams come first.

## 1. Streams

```hcl
resource "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs" # logs | metrics | traces | metadata

  data_retention        = 30  # days; 0 inherits the cluster default
  max_query_range       = 168 # hours; 0 is unlimited
  full_text_search_keys = ["message"]
  index_fields          = ["level"]
  bloom_filter_fields   = ["trace_id"]
  defined_schema_fields = ["service", "level", "message"]
  distinct_value_fields = ["service"]

  partition_keys = [
    { field = "service", type = "value" },
  ]
}
```

### Create-or-adopt

Applying to a name that does not exist creates the stream. Applying to one that
already exists, typically because data was ingested into it, **adopts** it and
manages its settings.

The provider issues `POST /api/{org}/streams/{name}` and treats the resulting
`400 "stream already exists"` as success. This means a stream created by the
OTel collector can be brought under Terraform without recreating anything.

### A stream must exist before an alert can query it

Creating an alert against a stream that does not exist fails with a bare 404:

> `HTTP 404: {"code":404,"message":"Stream k8s_events not found"}`

In a running system ingestion creates streams. In a fresh organization, declare
them explicitly before the alerts that reference them.

### The server renames your stream

OpenObserve replaces every character outside `[a-zA-Z0-9_:]` with an underscore,
so a stream created as `app-logs` is stored as `app_logs`. It may also lowercase
the name, depending on `ZO_FORMAT_STREAM_NAME_TO_LOWER`.

`effective_name` reports what was actually stored:

```hcl
resource "openobserve_stream" "app_logs" {
  name        = "app-logs"
  stream_type = "logs"
}

output "queried_as" {
  value = openobserve_stream.app_logs.effective_name # "app_logs"
}
```

This matters beyond cosmetics. Alerts, dashboards and SQL must reference the
stored name, so an alert querying `FROM "app-logs"` finds nothing. **Write
stream names in their normalized form** and the whole problem disappears.

The normalization is also inconsistent server-side: create, schema, settings and
update all normalize the name they are given, but the delete endpoint does not.
The provider resolves the stored name before deleting, so this is handled, but
a hand-rolled `curl` delete against the name you configured will answer 404 for
a stream that plainly exists.

### Settings are deltas

`PUT /api/{org}/streams/{name}/settings` takes every list setting as
`{add, remove}`, not as an absolute value:

```json
{
  "partition_keys":        {"add": [{"field": "service", "types": "value"}], "remove": []},
  "full_text_search_keys": {"add": ["message"], "remove": []},
  "index_fields":          {"add": ["level"], "remove": []},
  "data_retention": 30
}
```

Sending absolute arrays is rejected. The provider GETs current settings, diffs
against the configuration, and sends the difference, which is why this resource
reads before every write.

### Read and write shapes differ

Partition keys are **sent** as an array and **returned** as a map keyed by level:

```json
"partition_keys": {
  "L0": {"field": "service", "types": "value", "disabled": true},
  "L1": {"field": "region",  "types": "value", "disabled": false}
}
```

Older builds return an array. The provider decodes both, ordering `L0` before
`L10` numerically rather than lexically.

Note `disabled`. **Removing a partition key does not delete it**; the server
retires it so old data stays readable. The provider treats a disabled key as
absent.

Distinct value fields vary similarly: objects on current builds, bare strings on
older ones.

### Two traps

**A partition key cannot also be a secondary index field.**

> `partition key [service] cannot also be a secondary index field`

Keep the two lists disjoint.

**`index_fields` is a superset of what you configure.** OpenObserve registers
every bloom filter field as a secondary index field, so a stream configured with
`bloom_filter_fields = ["trace_id"]` reads back with `trace_id` in `index_fields`
too. The provider treats a server superset as **not** drift; otherwise every plan
would show a diff you could not resolve.

### Partition strategies

| `type` | Wire form |
|---|---|
| `value` | `"value"` |
| `prefix` | `"prefix"` |
| `hash` | `{"hash": 16}` |

```hcl
partition_keys = [
  { field = "service", type = "value" },
  { field = "tenant",  type = "hash", hash_buckets = 16 },
]
```

`hash_buckets` applies only to `hash`.

### Deletion is asynchronous

A stream already queued for deletion answers `400 "stream [name] is being
deleted"` rather than 404. The provider treats that as success so a retried
destroy does not fail.

### Reading streams

```hcl
data "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs"
}

output "schema" {
  value = data.openobserve_stream.app_logs.schema
}

data "openobserve_streams" "all" {
  stream_type = "logs"
}
```

The single data source exposes `schema`, `doc_num`, `storage_size`,
`compressed_size` and the settings, which is the fastest way to see what
ingestion has actually created.

### Import

```bash
terraform import openobserve_stream.app_logs default/logs/app_logs
```

The import ID is `{org_id}/{stream_type}/{name}`.

---

## 2. Folders

Folders group dashboards, alerts, reports and synthetics.

```hcl
resource "openobserve_folder" "platform" {
  name = "Platform" # folder_type defaults to "dashboards"
}

resource "openobserve_folder" "reliability" {
  folder_type = "alerts" # also holds SLOs
  name        = "Reliability"
  description = "Objectives and the alerts on them"
}
```

`folder_type` is one of `dashboards`, `alerts`, `reports`, `synthetics`.

> **SLOs live in alert folders.** There is no SLO folder type. An
> `openobserve_slo` takes a `folder_id` from an `openobserve_folder` with
> `folder_type = "alerts"`.
>
> **Synthetic checks do not.** They have a type of their own and must use it.
> An `openobserve_synthetic` pointed at an `alerts` folder fails with an opaque
> `FOREIGN KEY constraint failed (787)` that names nothing useful.

`folder_type` is `RequiresReplace`: a folder cannot change what it holds.

### Wire details

Folder endpoints are `/api/v2/{org}/folders/{folder_type}` and use **camelCase**
on the wire (`folderId`), unlike most of the API.

Folder IDs are server-assigned snowflake-style integers as strings, for example
`7494175832848465920`. They are not KSUIDs, unlike alert and SLO IDs.

### The default folder

Every organization has a folder literally named `default`, and every resource
with a `folder_id` defaults to it. Ordinary alerts create the default folder on
demand if it is missing.

> **Composite alerts do not.** A composite requires its folder to already exist
> and fails with `composite_folder_not_found` otherwise.

### Reading folders

```hcl
data "openobserve_folder" "reliability" {
  folder_type = "alerts"
  name        = "Reliability"
}

data "openobserve_folders" "alert_folders" {
  folder_type = "alerts"
}
```

Looking a folder up by name is the usual way to attach to one created in the UI.

### Import

```bash
terraform import openobserve_folder.platform default/dashboards/7123abc
```

The import ID is `{org_id}/{folder_type}/{folder_id}`.

---

## 3. Organizations

```hcl
resource "openobserve_organization" "team" {
  name = "Platform Team"
}
```

`identifier` is computed and is what other resources pass as `org_id`.

> **OpenObserve exposes no organization delete API.** Destroying the resource
> removes it from state and emits a warning; the organization stays on the
> server. This is a server limitation, not a provider one.

```hcl
resource "openobserve_stream" "team_logs" {
  org_id      = openobserve_organization.team.identifier
  name        = "team_logs"
  stream_type = "logs"
}
```

`org_id` is `RequiresReplace` on every resource: an object cannot move between
organizations, so changing it recreates.
