---
name: openobserve-terraform
description: Write, review, and debug Terraform or OpenTofu configuration for OpenObserve using the openobserve/openobserve provider. Covers streams, folders, dashboards, alerts (SQL, PromQL, aggregation, SLO, composite), service level objectives, pipelines, VRL functions, pipeline destinations, synthetic monitoring, ingestion tokens, users, service accounts, roles, and groups, plus import, drift, and decoding OpenObserve API errors. Use whenever a task involves the openobserve Terraform provider, an openobserve_* resource or data source, or managing OpenObserve configuration as code.
---

# OpenObserve Terraform provider

Everything here was verified against a live OpenObserve server. Where a rule is
stated, it is because the server rejected the alternative.

## Before writing anything

Two questions decide most of the work:

1. **Which resource?** See the inventory below.
2. **Is this open source or Enterprise, and which features are switched on?**
   Roles and groups need Enterprise with OpenFGA. Synthetics needs
   `ZO_SYNTHETICS_ENABLED=true` on either edition. Everything else, including
   SLOs, works on both with no flag.

If the answer to either is unclear from the task, read the relevant reference
file before writing HCL. Guessing at the shape of `query_condition`, an SLO
indicator, or a dashboard panel is the single most common way to produce
configuration that plans cleanly and then fails on apply.

## Provider setup

```hcl
terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.4"
    }
  }
}

provider "openobserve" {
  endpoint = "https://openobserve.example.com"
  username = "admin@example.com"
  password = var.oo_password
  org_id   = "default"
}
```

Prefer the environment for credentials: `OPENOBSERVE_ENDPOINT`,
`OPENOBSERVE_USERNAME`, `OPENOBSERVE_PASSWORD`, `OPENOBSERVE_ORG_ID`. Every
resource takes an optional `org_id` that falls back to the provider's, and it is
`RequiresReplace` everywhere because an object cannot move between organizations.

The provider is published to both registries:

- Terraform: `registry.terraform.io/providers/openobserve/openobserve`
- OpenTofu: `search.opentofu.org/provider/openobserve/openobserve`

The `source` address is the same for both. It speaks plugin protocol 6.0, so it
needs Terraform 1.0+ or any OpenTofu version.

## Resource inventory

18 resources:

| Resource | Purpose |
|---|---|
| `openobserve_organization` | Organizations. Create and rename only; there is no delete API |
| `openobserve_stream` | Streams: retention, partitioning, indexing |
| `openobserve_folder` | Folders for dashboards and alerts. SLOs live in **alert** folders |
| `openobserve_dashboard` | A dashboard from a JSON document, any schema version |
| `openobserve_user` | Users and their membership of an organization |
| `openobserve_service_account` | Service accounts with API tokens and rotation |
| `openobserve_role` † | Custom roles and their permissions |
| `openobserve_group` † | User groups and the roles they grant |
| `openobserve_alert_template` | Notification message templates |
| `openobserve_alert_destination` | Webhook, email, and SNS destinations |
| `openobserve_alert` | Scheduled and real-time alerts (SQL, PromQL, aggregation, SLO) |
| `openobserve_composite_alert` | Alerts that combine other alerts through a boolean expression |
| `openobserve_slo` | Service level objectives |
| `openobserve_function` | VRL or JavaScript transforms used by pipelines |
| `openobserve_pipeline_destination` | An external endpoint a pipeline forwards to |
| `openobserve_pipeline` | A graph that transforms records in flight |
| `openobserve_ingestion_token` | A credential for a collector or agent sending data in |
| `openobserve_synthetic` ‡ | An HTTP, TCP, TLS, SSH, or browser check run from probe locations |

33 data sources: singular and plural forms of the above, plus
`openobserve_user_roles`, `openobserve_resources` †,
`openobserve_composite_alert_references`, `openobserve_pipelines`, and
`openobserve_synthetic_locations` ‡.

† Enterprise with OpenFGA (`ZO_OPENFGA_ENABLED=true`). On open source these
return a diagnostic naming the feature, not a bare HTTP 403.

‡ Requires `ZO_SYNTHETICS_ENABLED=true`. When it is off the routes are not
registered at all, so every path answers 404 and a missing check is
indistinguishable from a disabled feature. The provider detects this and says
which it is.

Every resource supports `terraform import`.

## Where to read next

Load the reference file for the area being worked on. They are detailed on
purpose; do not reconstruct their content from memory.

| Task | Read |
|---|---|
| Streams, retention, partitioning, folders | `references/streams-and-folders.md` |
| Dashboards, panel JSON, chart bindings | `references/dashboards.md` |
| Any alert: SQL, PromQL, aggregation, thresholds, scheduling, per-group | `references/alerts.md` |
| Alerts that combine other alerts | `references/composite-alerts.md` |
| SLOs, indicators, error budgets, burn-rate alerts | `references/slos.md` |
| Pipelines, VRL functions, pipeline destinations | `references/pipelines.md` |
| Synthetic checks, the check budget, browser journeys | `references/synthetics.md` |
| Ingestion tokens, and why they cannot be deleted | `references/ingestion-tokens.md` |
| Users, service accounts, roles, groups, permissions | `references/iam.md` |
| Importing existing objects, or a plan that will not settle | `references/import-and-drift.md` |
| An API error you want decoded | `references/errors.md` |
| Installing the community alert library | `references/alert-library.md` |

Runnable configuration is in `examples/`. `scripts/dev-server.sh` starts a
throwaway OpenObserve in Docker for verifying a change against a real server.

## The mental model

OpenObserve's API is not uniformly shaped, and the provider exists largely to
absorb that. Four patterns recur, and recognising them explains most of the
provider's behaviour:

**Deltas, not desired state.** Stream list settings and role permissions take
`{add, remove}` rather than an absolute value, so those resources read before
every write.

**Adjacently tagged enums.** A discriminant and its payload sit in sibling keys:
`{"sli_type": "count", "config": {...}}`. SLO indicators do this twice over.

**Separate endpoints for single fields.** Enabling an SLO, moving an alert
between folders, and rotating a service account token are each their own
endpoint. Writing the field into the update body silently does nothing.

**Read and write shapes differ.** Partition keys are sent as an array and
returned as a map keyed by level. Dashboards are sent bare and returned inside a
versioned envelope. Composite expressions are sent as written and stored
canonicalized.

## Rules that prevent most failures

These are the ones worth knowing before reading anything else.

**An alert needs a destination, and a destination needs a template.** A
destination created without a template becomes a pipeline destination and cannot
be used by an alert. Always create the pair.

**A stream must exist before an alert can query it.** Creating an alert against
a stream that does not exist fails with a bare `HTTP 404: Stream <name> not
found`. In a live system ingestion creates streams; in a fresh org, declare them.

**SLOs live in alert folders. Synthetics do not.** There is no SLO folder type,
so `folder_id` on an `openobserve_slo` refers to a folder with
`folder_type = "alerts"`. Synthetics *do* have their own type, and pointing a
check at an alert folder fails with an opaque
`FOREIGN KEY constraint failed (787)`. The four types are `dashboards`,
`alerts`, `reports`, `synthetics`.

**An SLO alert has no count gate.** Omit `trigger_condition.threshold` and
`operator` entirely. Every other family uses them; this one rejects them.

**A composite alert accepts no query or schedule.** No `stream_name`, no
`query_condition`, no `period`, `frequency` or `threshold`. `silence` is the
only scheduling attribute it takes.

**A partition key cannot also be a secondary index field.** Keep the two lists
disjoint.

**A pipeline's function and destination must be referenced, not named.** Writing
`openobserve_function.x.name` rather than `"x"` is what orders creation and
teardown. The server refuses to delete either while a pipeline still uses it.

**Role and group names outside `[a-zA-Z0-9_]` are silently rewritten** by the
server. The provider rejects them during `plan` rather than tracking a name that
does not exist.

**Threshold `value` is a string in HCL.** The provider sends it as a JSON number
when it parses as one and a JSON string otherwise, so `value = "100"` is the
number 100 and `value = "error"` is the string. An aggregation's `having.value`
must be numeric.

**Expect one diff on the first plan after `terraform import`.** Import populates
state from the server with no configuration to compare against. Applying
converges and recreates nothing.

**An ingestion token cannot be deleted, and its name cannot be reused.** There
is no delete endpoint, so removing the resource disables the token instead and
leaves the record behind. Because names are unique, re-adding a token with the
same name then fails. Cycling a credential means a new name, not the old one.

**A deleted stream blocks its own name for up to an hour.** Deletion marks the
stream and the data retention job clears the marker, on
`ZO_COMPACT_DATA_RETENTION_INTERVAL` (3600s by default). Until then, recreating
it fails with `stream [name] is being deleted`. This makes destroy-then-apply
cycles fail on streams, which is expected rather than a provider bug.

**A synthetic check's run time is capped by a fixed budget, not by its
schedule.** `ZO_SYNTHETICS_MAX_CHECK_BUDGET_SECS` (840s) bounds
`combos x (attempts x per_attempt + retries x wait_before_retry_secs)`. Running
a check hourly does not buy it more room than running it every minute.

**Comparison operators on alert conditions are PascalCase.** `Contains`,
`NotContains`, `IsNull`, `IsNotNull`, `IsEmpty`, `IsNotEmpty`. The snake_case
spellings are rejected. The four `Is*` operators are unary: omit `value`.

## Verifying work

The provider's own test suite treats a change as unverified until five steps
pass. Apply the same standard to configuration:

1. `terraform apply` succeeds
2. `terraform apply` again reports no changes
3. `terraform plan` reports no changes
4. `terraform state rm` then `terraform import`, then one apply, then a clean plan
5. `terraform destroy` succeeds, and re-running it is not an error

Most bugs surface at steps 3 and 4, not step 1. A configuration that applies once
is not a working configuration.

Against a local single-node server, use `terraform apply -parallelism=1`. SQLite
`code: 517` is metastore write contention, not an API error.

## Style when writing configuration for a user

- Reference resources rather than hardcoding IDs. `openobserve_alert.x.alert_id`
  is what tells Terraform about ordering; a literal ID silently loses it, which
  matters most for composite alerts and SLO alerts.
- Put alerts and SLOs in a named folder rather than `default`. Folders are how
  OpenObserve scopes permissions.
- Set `tags` and `priority` on alerts. They cost nothing and make a large
  installation navigable.
- Do not set `owner`, `last_triggered_at`, `updated_at` or other server-managed
  fields. They are computed.
