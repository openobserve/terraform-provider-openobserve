# Import, adoption and drift

## 1. Every resource supports import

```bash
terraform import openobserve_organization.example      my-org
terraform import openobserve_stream.example            default/logs/app_logs
terraform import openobserve_folder.example            default/dashboards/7123abc
terraform import openobserve_dashboard.example         default/7123abc
terraform import openobserve_user.example              default/user@example.com
terraform import openobserve_service_account.example   default/ci@example.com
terraform import openobserve_role.example              default/analyst
terraform import openobserve_group.example             default/sre
terraform import openobserve_alert_template.example    default/slack
terraform import openobserve_alert_destination.example default/pagerduty
terraform import openobserve_alert.example             default/2fXkZ8QlmNbYcV1pR3sT
terraform import openobserve_composite_alert.example   default/2fXkZ8QlmNbYcV1pR3sT
terraform import openobserve_slo.example               default/2fXkZ8QlmNbYcV1pR3sT
terraform import openobserve_function.example          default/redact_email
terraform import openobserve_pipeline_destination.example default/warehouse
terraform import openobserve_pipeline.example          default/7497861055431835648
```

Two ID shapes are in play. Alerts, composites and SLOs use **KSUIDs**
(`2fXkZ8QlmNbYcV1pR3sT`). Folders, dashboards and pipelines use
**snowflake-style integers as strings** (`7494175832848465920`).

Functions and pipeline destinations are addressed by name rather than by an
identifier, so their import IDs are `{org_id}/{name}`.

## 2. Finding identifiers

The data sources are the fastest way:

```hcl
data "openobserve_alerts" "all" {}
data "openobserve_slos" "all" {}
data "openobserve_dashboards" "all" {}
data "openobserve_folders" "alerts" { folder_type = "alerts" }
data "openobserve_pipelines" "all" {}

output "alert_ids" {
  value = { for a in data.openobserve_alerts.all.alerts : a.name => a.alert_id }
}

output "composite_ids" {
  value = {
    for a in data.openobserve_alerts.all.alerts :
    a.name => a.alert_id if a.alert_type == "composite"
  }
}

output "slo_ids" {
  value = { for s in data.openobserve_slos.all.slos : s.name => s.slo_id }
}

# A pipeline's identifier appears nowhere in configuration, so this is the only
# way to find what an import needs.
output "pipeline_ids" {
  value = { for p in data.openobserve_pipelines.all.pipelines : p.name => p.pipeline_id }
}
```

## 3. Expect one diff on the first plan after import

Import populates state from the server with no configuration to compare against,
so the first plan reconciles what you wrote against what the server stored.
**Applying converges and recreates nothing.**

This was verified for every resource type: import, apply once, then a clean
plan.

The clearest example is a composite alert's `expression`. The server stores a
fully parenthesized rewrite, and after import there is no configured spelling to
preserve, so the first plan shows the canonical form changing to yours.

If a plan after import wants to **replace** rather than update, something is
genuinely different: usually `org_id`, `stream_type` or `stream_name`, all of
which are `RequiresReplace`.

## 4. Adopting rather than recreating

Several resources adopt an existing object instead of failing:

| Resource | Adoption behaviour |
|---|---|
| `openobserve_stream` | `400 "stream already exists"` treated as success |
| `openobserve_user` | `409 "User is already part of the organization"` treated as success |
| `openobserve_function` | `400 "Function already exist"` falls through to the update endpoint |

To move an existing resource onto a new schema without touching the server:

```bash
terraform state rm openobserve_stream.app_logs
terraform import openobserve_stream.app_logs default/logs/app_logs
```

## 5. How the provider decides what counts as drift

Three reconciliation strategies, each solving a real problem. Knowing which one
applies explains most "why is this not showing a diff" questions.

**JSON subset (dashboards).** The server enriches a stored dashboard with
`dashboardId`, `created` and others. If the server's document still contains
everything the configured document asked for, the configured value stays in
state. Change a value the server disagrees with and the difference surfaces
normally.

**Set superset (stream lists).** OpenObserve derives some list settings from
others, so the server's `index_fields` can be a strict superset of what was
configured. A superset is not drift; anything else is.

**Spelling preservation (role permissions, composite expressions, VRL bodies).**
`stream` and `stream:_all_myorg` name the same grant; `{a} && {b}` and
`({a} && {b})` are the same expression; a VRL body with and without the trailing
`.` the server appends is the same program. The server only reports one form.
Whichever spelling was configured is kept when the two describe the same thing.

A pipeline's condition node uses the JSON subset rule instead, because
`jsonencode()` sorts keys alphabetically while the server returns them in struct
order.

### Optional booleans

Terraform requires the value applied to an Optional attribute to match
configuration exactly, so writing `false` where the user wrote nothing is an
inconsistent-result error. The provider leaves a server `false` as null when the
attribute was unset, and reports anything else.

This is why `ignore_case`, `multi_alert` and similar optional flags do not
appear in state when you did not set them.

## 6. Server-managed fields

Do not set these; they are computed and setting them either errors or is
ignored:

`id`, `alert_id`, `slo_id`, `folder_id` (when computed), `owner`,
`last_triggered_at`, `last_satisfied_at`, `updated_at`, `last_edited_by`,
`definition_generation`, `scheduler_job_present`, `child_alert_ids`,
`is_default` on templates, `token` on service accounts, `effective_name` on
streams, `num_args` on functions, and `pipeline_id`, `version` and every node's
`io_type` and position on pipelines.

## 7. A plan that will not settle

Work through these in order.

1. **Two resources own one fact.** The most common cause. `openobserve_role.users`
   and `openobserve_user.custom_roles` write the same grant; using both means
   each removes what the other adds. Pick one direction.

2. **A `RequiresReplace` attribute changed.** `org_id`, `stream_type`,
   `stream_name` on alerts.

3. **Runtime state read through a data source.** `openobserve_composite_alert`
   exposes `evaluation.evaluated_at`, which moves on every evaluation. That is an
   output diff, not resource drift, and is expected.

   A related trap: a data source that reports on a *relationship* only depends
   on whatever it interpolates. `openobserve_composite_alert_references` takes
   the child's `alert_id`, so Terraform is free to read it before the composites
   exist, and it returns an empty list on the first apply. Add an explicit
   `depends_on` covering the other end of the relationship.

4. **A field the server rewrites.** Role and group names outside `[a-zA-Z0-9_]`
   are rewritten server-side; the provider blocks these during `plan`, but a
   pre-existing resource imported with such a name will never settle. Rename it.

5. **A stale local metastore.** Against a single-node dev server, SQLite
   `code: 517` is write contention rather than an API error. Use
   `terraform apply -parallelism=1`.

## 8. The verification lifecycle

A change is not verified until all five pass:

1. `terraform apply` succeeds
2. `terraform apply` again reports no changes
3. `terraform plan` reports no changes
4. `terraform state rm` then `terraform import`, then one apply, then a clean plan
5. `terraform destroy` succeeds, and re-running it is not an error

Most bugs surface at steps 3 and 4. A configuration that applies once is not a
working configuration.

For destroy ordering specifically, remember that a composite alert blocks the
deletion of any alert it names as a child. Terraform gets that ordering right
only if the expression interpolates the child's `alert_id`.
