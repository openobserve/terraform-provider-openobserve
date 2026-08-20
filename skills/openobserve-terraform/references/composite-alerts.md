# Composite alerts

`openobserve_composite_alert` fires on a boolean expression over other alerts'
current states rather than on a query of its own. Added in provider 1.2.0.

The point is actionability. "The error rate is high" pages whether or not anyone
can act on it. "The error rate is high **and** a deploy went out in the last
thirty minutes" is a page with an obvious next step. Both facts already exist as
alerts; a composite only says how they combine.

```hcl
resource "openobserve_composite_alert" "bad_deploy" {
  name         = "checkout-bad-deploy"
  folder_id    = openobserve_folder.reliability.folder_id
  destinations = [openobserve_alert_destination.slack.name]
  description  = "Checkout is erroring right after a deploy"

  expression = "{${openobserve_alert.error_rate.alert_id}} && {${openobserve_alert.recent_deploy.alert_id}}"

  silence = 30
}
```

A composite never re-runs its children's queries. It reads the state they
already computed, so it costs nothing to evaluate and cannot disagree with what
the children's own pages said.

---

## 1. What a composite does NOT accept

This is the part that catches people. A composite is re-evaluated when a child
changes state, so it has no schedule of its own. The server does not ignore
scheduling fields, it **rejects** them with `composite_unsupported_field`,
naming the first offender.

Rejected: `anomaly_config`, `id`, `stream_name`, `query_condition`,
`row_template`, `is_real_time`, `deduplication`, `tz_offset`, and every
`trigger_condition` field except `silence`, meaning `period`, `operator`,
`threshold`, `warning_threshold`, `notify_on_warning`, `frequency`,
`frequency_type`, `cron`, `timezone`, `tolerance_in_secs`.

The provider's schema simply has no attributes for any of these, so this is a
compile-time fact rather than a runtime surprise. **`silence` is the only
scheduling attribute.**

## 2. Full attribute list

| Attribute | Required | Notes |
|---|---|---|
| `name` | yes | Unique within the folder |
| `expression` | yes | Boolean expression over child alert IDs |
| `folder_id` | no | Defaults to `default`. The folder must already exist |
| `warning_counts_as_firing` | no | Default `true` |
| `stale_child_policy` | no | Default `use_last_state` |
| `enabled` | no | Default `true` |
| `description` | no | |
| `destinations` | no | Names of `openobserve_alert_destination` resources |
| `template` | no | Overrides each destination's own template |
| `context_attributes` | no | Key/value map for the template |
| `silence` | no | Minutes quiet after firing. Default 0 |
| `creates_incident` | no | Default `false` |
| `workflows` | no | Workflow IDs |
| `priority` | no | 1 to 5 |
| `tags` | no | Selection tags |
| `child_alert_ids` | computed | Children the server resolved, in expression order |
| `scheduler_job_present` | computed | Whether a scheduler job currently backs it |
| `alert_id` | computed | Shares the alert ID namespace, so a composite can be another composite's child |

Note there is no `owner` attribute. The composite detail endpoint does not
report one, so the provider does not model a field it could never verify.

---

## 3. The expression grammar

```
or    := and ( "||" and )*
and   := unary ( "&&" unary )*
unary := "!" unary | atom
atom  := "{" alert_id "}" | "(" or ")"
```

`&&` binds tighter than `||`, and `!` tighter than both, as in most languages.
Parentheses override that.

Operands are brace-wrapped alert IDs, which is what makes Terraform
interpolation natural:

```hcl
expression = "{${openobserve_alert.errors.alert_id}} && !{${openobserve_alert.maintenance.alert_id}}"
```

### Constraints checked by the provider during `plan`

| Rule | Value |
|---|---|
| Minimum children | 2. A composite of one is just the child |
| Maximum children | 10 |
| Duplicate operands | Rejected, not deduplicated |
| Maximum expression size | 4096 bytes |

A syntax error, a duplicate operand, or a child count outside those bounds comes
back as a diagnostic on the offending line rather than an apply-time HTTP 400:

```
Error: Invalid composite expression

  18:   expression = "{2abc...} && {2abc...}"

child `2abc...` referenced more than once
```

### Constraints checked only by the server

Whether each child exists and is readable, whether a child is eligible, and
whether the reference graph stays acyclic and within depth 2. These need the
organization's alert graph, which only the server holds.

---

## 4. The canonicalization trap

**The server does not store the expression you send.** It reparses and stores a
fully parenthesized rewrite:

| Sent | Stored |
|---|---|
| `{a} && {b}` | `({a} && {b})` |
| `{a} \|\| {b} && {c}` | `({a} \|\| ({b} && {c}))` |
| `!{a} && {b}` | `((!{a}) && {b})` |
| `{a} && {b} && {c}` | `(({a} && {b}) && {c})` |

The provider parses both sides and compares the trees, so your spelling is
preserved and equivalent parenthesization is never reported as drift.

The one place this leaks: after `terraform import` there is no configured
spelling to keep, so state holds the server's form and the first plan shows it
changing to yours. Applying converges and changes nothing server-side. This is
the same one-time reconciliation every other resource has after an import.

> A plan modifier cannot paper over that, and trying is a trap worth naming.
> `expression` is `Required`, and a plan modifier that overrides a Required
> attribute's configured value leaves Terraform proposing the identical update
> on every plan, forever.

---

## 5. Eligible children

Scheduled alerts, SLO alerts, and other composites.

**Real-time alerts are not eligible.** They have no scheduled state for a
composite to read, and are rejected with `child_not_eligible`.

A composite may reference another composite, but only **one level deep**, and
never in a cycle. Depth 3 is rejected with `composite_too_deep`.

```hcl
# Legal: a composite of two composites.
resource "openobserve_composite_alert" "checkout_unhealthy" {
  name       = "checkout-unhealthy"
  folder_id  = openobserve_folder.reliability.folder_id
  expression = "{${openobserve_composite_alert.bad_deploy.alert_id}} || {${openobserve_composite_alert.unmitigated_errors.alert_id}}"
}
```

---

## 6. `warning_counts_as_firing` and `stale_child_policy`

These two decide what each child contributes, and the defaults are opinionated
enough to be worth setting deliberately.

**`warning_counts_as_firing`** (default `true`) decides whether a child sitting
at `warning` counts as true. Set `false` for a composite that should only react
to children at `critical`.

**`stale_child_policy`** decides what a child contributes once **stale**, meaning
it has not been evaluated within three times its own cadence
(`ZO_ALERT_COMPOSITE_STALE_K`, default 3):

| Value | A stale child | Use when |
|---|---|---|
| `use_last_state` (default) | keeps contributing its last reported value | you trust the frozen value |
| `treat_as_false` | stops satisfying the expression | a broken child must not hold a composite firing |
| `treat_as_true` | satisfies the expression | absence of a heartbeat is itself the signal |

The default quietly picks "trust the frozen value". For a composite whose whole
job is to catch a failure, that is often the wrong answer:

```hcl
resource "openobserve_composite_alert" "capacity_lost" {
  name       = "k8s-capacity-lost"
  expression = "{${openobserve_alert.node_not_ready.alert_id}} && {${openobserve_alert.pod_pending.alert_id}}"

  # A node that stops reporting is exactly the case this alert exists for, so a
  # stale child must not quietly stop satisfying the expression.
  stale_child_policy = "treat_as_true"
}
```

---

## 7. Destroy ordering

The server refuses to delete an alert while a composite names it as a child:

```json
{"code":"child_referenced",
 "message":"this alert is referenced by one or more composite alerts",
 "references":[{"alert_id":"...","name":"comp_bad_deploy","folder_id":"..."}],
 "hidden_reference_count":0}
```

**Interpolating `openobserve_alert.x.alert_id` into the expression is what tells
Terraform the composite depends on the child**, so children are created first
and destroyed last. If the expression is built from a hardcoded ID or a variable
instead, Terraform does not know about the dependency and the destroy fails.

When a destroy is blocked by something outside the configuration:

```hcl
data "openobserve_composite_alert_references" "error_rate" {
  alert_id = openobserve_alert.error_rate.alert_id

  # Without this the data source only depends on the child, so Terraform is
  # free to read it before the composites exist and it returns an empty list.
  # Any data source reporting on a relationship needs to depend on both ends.
  depends_on = [openobserve_composite_alert.bad_deploy]
}

output "blocking_composites" {
  value = [for r in data.openobserve_composite_alert_references.error_rate.references : r.name]
}
```

`hidden_reference_count` matters: permissions can hide referencing composites,
so an empty `references` list does not prove the alert is unreferenced.

---

## 8. Inspecting a composite

```hcl
data "openobserve_composite_alert" "bad_deploy" {
  name = "checkout-bad-deploy"
}

output "children_currently_true" {
  value = [for c in data.openobserve_composite_alert.bad_deploy.children : c.name if c.truth]
}

output "stale_children" {
  value = [for c in data.openobserve_composite_alert.bad_deploy.children : c.name if c.stale]
}

output "last_result" {
  value = try(data.openobserve_composite_alert.bad_deploy.evaluation.result, null)
}
```

`children` reports what the composite actually read, per child: `alert_id`,
`accessible`, `name`, `alert_type`, `folder_id`, `enabled`, `level`, `level_at`,
`stale`, `truth`.

`evaluation` is the composite's own last verdict (`result`, `level`,
`evaluated_at`) and is **null until it has run once**, which is not the same as
having evaluated to false. Use `try()` when reading it.

**A composite that will not fire is almost always explained by one of two
things:** a child is stale under a `stale_child_policy` other than
`use_last_state`, or a child is at `warning` while `warning_counts_as_firing` is
`false`.

---

## 9. Server-side switches

| Situation | Error code |
|---|---|
| `ZO_ALERT_COMPOSITE_WRITES_ENABLED=false` | `composite_writes_disabled` |
| Super-cluster deployment | `composite_super_cluster_unsupported` |
| Concurrent composite writes in one org | `composite_graph_lock_unavailable` |
| Folder does not exist | `composite_folder_not_found` |

Composite writes are enabled by default; the env var is an opt-out kill switch.

Composite writes serialize on a per-organization graph lock, so creating several
at once can fail. Apply with `-parallelism=1` when creating many.

> Unlike an ordinary alert, a composite does **not** create its folder on
> demand. Create the `openobserve_folder` first and reference its `folder_id`.

---

## 10. Worked patterns

Three correlations that are hard to express any other way:

```hcl
# Node memory pressure alone is noise. Node memory pressure while pods are being
# OOM killed is a capacity incident.
expression = "{${openobserve_alert.node_memory_pressure.alert_id}} && {${openobserve_alert.pod_oom_killed.alert_id}}"

# Crashlooping is only worth paging on when it is not already explained by an
# image pull problem, which has a different fix and its own alert.
expression = "{${openobserve_alert.crashloop.alert_id}} && !{${openobserve_alert.image_pull_backoff.alert_id}}"

# Either of two independent symptoms of the same underlying failure.
expression = "{${openobserve_alert.queue_depth.alert_id}} || {${openobserve_alert.consumer_lag.alert_id}}"
```

## 11. Import

```bash
terraform import openobserve_composite_alert.bad_deploy default/2fXkZ8QlmNbYcV1pR3sT
```

Find the ID with the `openobserve_alerts` data source, filtering on
`alert_type == "composite"`.
