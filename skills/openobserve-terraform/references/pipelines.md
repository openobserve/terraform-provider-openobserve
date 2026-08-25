# Pipelines, functions and pipeline destinations

Three resources that only make sense together. A pipeline transforms records in
flight; a function is the transform; a pipeline destination is somewhere outside
OpenObserve to send the result.

Added in provider 1.3.0.

---

## 1. The dependency that shapes everything

The server refuses to delete a function or a destination that a pipeline still
references:

```json
{"code":409,"message":"Warning: Function 'redact_email' has 1 pipeline dependencies.
 Please remove these pipelines first: [{\"id\":\"749786...\",\"name\":\"redact_pii\"}]"}

{"code":409,"message":"Destination is currently used by pipeline: ship_audit"}
```

**Reference the resource, never the literal name.** That is what makes Terraform
create them in the right order and tear them down in the reverse:

```hcl
node {
  id            = "redact"
  type          = "function"
  function_name = openobserve_function.redact_email.name   # not "redact_email"
}
```

With a literal string Terraform sees no dependency, creates them in any order,
and the teardown fails. This is the same shape as the composite alert
`child_referenced` guard; see `composite-alerts.md`.

---

## 2. Functions

```hcl
resource "openobserve_function" "redact_email" {
  name = "redact_email"

  function = <<-VRL
    .email = "[redacted]"
    .redacted_at = now()
  VRL
}
```

The server compiles the body when it is saved, so a syntax error fails
`terraform apply` rather than surfacing later as a pipeline that quietly stopped
working.

| Attribute | Notes |
|---|---|
| `name` | Unique per organization. `RequiresReplace` |
| `function` | The body. Use a heredoc |
| `params` | Comma-separated parameter names, default `row` |
| `language` | `vrl` (default) or `js` |
| `num_args` | Computed from `params` |

### The trailing dot

A VRL program's value is its last expression, and a transform has to return the
record, so the server appends ` \n .` to any VRL body that does not already end
in `.`:

```
sent:   .email = "[redacted]"
stored: .email = "[redacted]" \n .
```

The provider compares the two with that addition stripped, so it does not show
as drift. After `terraform import` there is no configured spelling to keep, so
the first plan shows it reconciling; applying converges.

### Creating is not upserting

`POST /functions` refuses an existing name with `400 "Function already exist"`
rather than overwriting. The provider adopts in that case by falling through to
the update endpoint, so re-applying over a function created in the UI works.

---

## 3. Pipeline destinations

```hcl
resource "openobserve_pipeline_destination" "warehouse" {
  name = "warehouse"
  url  = "https://warehouse.example.com/ingest"

  headers = {
    Authorization = "Bearer ${var.token}"
  }
}
```

This is the **same underlying object** as `openobserve_alert_destination`,
stored through the same endpoints. The server tells them apart by one field: a
destination carrying a `template` is an alert destination, one without is a
pipeline destination that alerts cannot use.

They are separate resources because that field decides what the object is for,
and because templates, email recipients and SNS do not apply to a pipeline.

> **The URL is resolved when the destination is saved.** A host that does not
> resolve is rejected with *Destination URL blocked by SSRF guard*, so the
> endpoint has to exist by the time Terraform applies.

`destination_type` names a system OpenObserve knows how to format for
(`openobserve`, `splunk`, `elasticsearch`, `custom`). Leave it unset for a plain
webhook.

---

## 4. Pipelines

A pipeline is a graph: records enter from a source stream, flow along edges
through nodes, and land in an output.

```hcl
resource "openobserve_pipeline" "redact" {
  name        = "redact_pii"
  stream_name = openobserve_stream.app_logs.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.app_logs.name
  }

  node {
    id            = "redact"
    type          = "function"
    function_name = openobserve_function.redact_email.name
  }

  node {
    id          = "out"
    type        = "stream"
    stream_name = openobserve_stream.app_logs_clean.name
  }

  edge {
    from = "in"
    to   = "redact"
  }

  edge {
    from = "redact"
    to   = "out"
  }
}
```

### Node types

| `type` | Needs | Does |
|---|---|---|
| `stream` | `stream_name` | Reads from or writes to a stream |
| `function` | `function_name` | Applies a transform |
| `condition` | `conditions` | Drops records that do not match |
| `remote_stream` | `destination_name` | Forwards to a pipeline destination |

A node block is flat with a `type` discriminator rather than one nested block
per kind. That is deliberate: Terraform materializes a single nested block whose
`dynamic` produced no iterations as a present object full of unknowns, which
makes "which variant did the author write" undecidable. A flat block avoids that
and keeps `dynamic "node"` working for generated graphs.

### Edges are multi-line

HCL allows only one argument on a single-line block, so this is a syntax error:

```hcl
edge { from = "in", to = "out" }   # Invalid single-argument block definition
```

Write them across lines.

### What the provider fills in

Three things the server requires but nobody should have to write:

- **`io_type`** is inferred from the edges: a node with nothing arriving is
  `input`, one with nothing leaving is `output`, anything else is `default`.
- **Positions** are laid out left to right in declaration order. They are purely
  cosmetic, but the server rejects a node without them.
- **Edge ids** follow the server's own `e{from}-{to}` convention, so a pipeline
  written here looks like one built in the UI.

All three can be set explicitly when a specific layout matters.

### Condition nodes

`conditions` is a JSON document. Use the v1 tree form, the same shape an alert's
`custom` conditions take:

```hcl
conditions = jsonencode({
  and = [
    { column = "level", operator = "=", value = "error", ignore_case = false },
  ]
})
```

`and`, `or` and `not` nest; the leaves are column/operator/value comparisons.
The server rejects an empty condition set.

`jsonencode()` sorts keys alphabetically while the server returns them in struct
order, so the provider compares the two as JSON rather than as text and keeps
your spelling.

### One realtime pipeline per source stream

> `A realtime pipeline with same source stream already exists`

A stream can be the source of only one realtime pipeline. Extend the existing
one rather than adding a second.

### Graph rules

Checked by the provider during `plan`:

- Node ids are unique
- Every edge endpoint names a declared node
- Each node type carries the field it needs
- At least two nodes, and at least one edge

Checked by the server: that the graph is connected and reachable from the input
node.

### Pausing

`enabled` is its own endpoint rather than a field on the update body. The
provider issues the extra call when the value changes, so from HCL it just
works.

---

## 5. Finding what to import

A pipeline's identifier is server-assigned and appears nowhere in configuration:

```hcl
data "openobserve_pipelines" "all" {}

output "pipeline_ids" {
  value = { for p in data.openobserve_pipelines.all.pipelines : p.name => p.pipeline_id }
}
```

```bash
terraform import openobserve_pipeline.redact         default/7497861055431835648
terraform import openobserve_function.redact_email   default/redact_email
terraform import openobserve_pipeline_destination.wh default/warehouse
```

`openobserve_function` as a data source reports `used_by`, which answers why a
delete was refused:

```hcl
data "openobserve_function" "redact" {
  name = "redact_email"
}

output "blocking_pipelines" {
  value = data.openobserve_function.redact.used_by
}
```

---

## 6. Names are normalized

The server trims a pipeline name and **lowercases** it, so `My Pipeline` is
stored as `my pipeline`. Writing names lowercase avoids the surprise. This is
the same class of trap as stream names; see `streams-and-folders.md`.
