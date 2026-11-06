# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`openobserve_downtime`** manages maintenance windows that mute alerts,
  anomaly detections and synthetics checks and correct SLOs. A downtime is
  one-time (`starts_at` to `ends_at`, RFC 3339 timestamps) or recurring
  (`daily` or `weekly`, with `start_time_local`, `duration_secs` and
  `weekdays` in a `timezone`). Each `target` block picks a module and narrows
  it by `folder_ids`, `ids` or, for synthetics, `tags`, with `slo_mode` for
  SLOs and `incident_mode` for alerts. `condition` narrows by dimensions, as a
  JSON document. `notifications` announces the downtime's events to alert
  destinations, and `show_banner` controls the org-wide banner. Destroying an
  active or scheduled downtime cancels it first, as the server requires; one
  that already corrected SLO slices is moved to the `default` folder and left
  to the server's retention, with a warning. A downtime that was ended or cancelled when last
  read stays in state after the server's retention deletes it, so a
  cancelled window is not created again. Changing `folder_id` moves the
  downtime in place.
  Most of the server's rules on schedules, targets and notifications are
  checked at plan time; the timezone name, condition keys and depth,
  destination names and the per-org cap are left to the server. Supports import as `{org_id}/{downtime_id}`. Requires
  OpenObserve Enterprise v1.2.0-rc1 or later.
- **`folder_type = "downtimes"`** on `openobserve_folder`, for downtime
  folders.
- **Recovery and on-call settings on `openobserve_alert`**:
  `recovery_destinations`, `keep_firing_for`, `suppress_repeats`,
  `runbook_url`, `oncall_team` and `query_condition.promql_rule_mode`. Until
  now the provider only preserved these across updates; now they can be set.
  Like `notify_on_recovery`, each one keeps the server's value while it is
  unset, so a value set in the UI is not reset by an unrelated change. Set
  `runbook_url` or `oncall_team` to `""` to clear it. The `openobserve_alert`
  data source reports the same settings, except `promql_rule_mode`.
  Combinations the server refuses are caught at plan time: any recovery
  setting on a real-time alert, `recovery_destinations` while recovery
  notifications are off (configured `false`, unset on a new alert, or off on
  the server), `keep_firing_for` outside 0 to 86400, a `runbook_url` that is
  not an `http(s)://` link, and rule mode on a non-PromQL alert, on a
  non-metrics stream, or together with a `promql_condition`, warning levels,
  an aggregation, `multi_time_range`, a trigger other than `>= 1` or `> 0`, a
  pending period without `promql_multi_alert = true`, or `creates_incident`
  with a pending period. The rule-mode checks also apply when rule mode was
  switched on in the UI and the attribute is unset. A server that silently
  drops a setting because it is too old for it now gets a message naming the
  setting and the server version it needs, instead of "Provider produced
  inconsistent result after apply": a warning when the alert is created, so
  that it is not tainted and recreated, and an error on update.
  `suppress_repeats` and `promql_rule_mode` need OpenObserve v1.2.0-rc1 or
  later, the others v1.1.0. `suppress_repeats` only takes effect on
  Enterprise, and `oncall_team` needs Enterprise with on-call.

## [1.5.0] - 2026-11-05

### Added

- **`openobserve_anomaly_alert`** manages OpenObserve anomaly alerts
  ([#5](https://github.com/openobserve/terraform-provider-openobserve/issues/5)).
  It sets the band width (`band_width`, or `alert_budget_per_day` to let the
  server pick it), `alert_direction`, and the alert window
  (`alert_window_buckets`, `alert_window_fire_pct`,
  `alert_window_recover_pct`). It exposes what training chose as
  `band_grouping`, `band_k`, `training_data_start_us` and
  `training_data_end_us`. Invalid ranges, `band_width` together with
  `alert_budget_per_day`, a recover percent above the fire percent and a
  `histogram_interval` off the server's bucket grid are rejected at plan time.
  Supports import as `{org_id}/{anomaly_id}`. Requires OpenObserve Enterprise
  v1.1.0 or later; against an open-source or older server the resource fails
  with a diagnostic saying so and leaves nothing behind. Changing `folder_id`
  moves the alert in place, as the UI does; the server keeps its RBAC
  ownership on the old folder.

- **`notify_on_recovery` on `openobserve_alert`**, and on the `openobserve_alert`
  data source ([#9](https://github.com/openobserve/terraform-provider-openobserve/issues/9)).
  Left unset, it keeps whatever the server holds across updates. Set, it is
  managed and drift shows in plan. An alert Terraform replaces starts from the
  server default unless the attribute is set.

- **`metadata` on `openobserve_alert_destination`**, and on its data source
  ([#9](https://github.com/openobserve/terraform-provider-openobserve/issues/9)).
  A map of strings, like `openobserve_pipeline_destination`. Left unset, the
  server's metadata is kept across updates; `{}` clears it. It is sensitive
  because OpenObserve keeps credentials such as a PagerDuty routing key there,
  and only webhook destinations store it.

- **`output_format` on `openobserve_pipeline_destination`**
  ([#10](https://github.com/openobserve/terraform-provider-openobserve/issues/10)).
  It takes `json`, `ndjson`, `nestedevent`, `esbulk` or `stringseparated`, and
  defaults to `json`, which is what the server sends for a destination saved
  without one. `esbulk` needs `output_format_index` and `stringseparated`
  needs `output_format_separator`. A destination whose format was set outside
  Terraform, for example `ndjson` in the UI, plans a change back to `json` on
  the first refresh after upgrading or on import unless the configuration sets
  `output_format`. Version 1.4.3 already reset it to `json` on any update.

- **`openobserve_pipeline` condition nodes accept v2 conditions**
  ([#11](https://github.com/openobserve/terraform-provider-openobserve/issues/11)).
  Write the v2 group in `conditions` with the same wrapper an alert's v2
  conditions use, `{ version = 2, conditions = { filterType = "group", ... } }`.
  The read-back is compared by meaning, so the server's key order and operator
  spelling do not show as a change. A document without `version` is v1 and is
  sent exactly as before, so existing condition nodes plan clean. Plan-time
  checks catch a v1 document marked as v2, a v2 group without the wrapper,
  keys the server would drop, and `conditions` on a node that is not a
  condition node. If you imported a v2 pipeline made in the UI under 1.4.3,
  the next plan shows its conditions in the wrapped form; wrap them the same
  way in your configuration.

### Fixed

- **Alert and destination updates reset settings the provider does not
  manage** ([#9](https://github.com/openobserve/terraform-provider-openobserve/issues/9)).
  OpenObserve replaces the whole object on update, so every apply switched
  recovery notifications off and cleared `recovery_destinations`,
  `keep_firing_for`, `suppress_repeats`, `runbook_url`, `oncall_team`,
  `incident_title_template` and a PromQL alert's `promql_rule_mode`. Left
  unset, `destinations`, `trigger_condition.operator`,
  `trigger_condition.threshold` and `aggregation.group_by` were also reset,
  which removed destinations added in the UI. On destinations it cleared
  `output_format` and `destination_type_name`, which left a prebuilt
  destination created in the UI still marked prebuilt but no longer formatted
  as one. The provider now reads these before each update and sends them back
  unchanged.

- **`openobserve_alert` did not read `folder_id` back for alerts outside the
  `default` folder when the user's access comes from folder grants**
  ([#16](https://github.com/openobserve/terraform-provider-openobserve/issues/16)).
  With RBAC and `list_only_permitted`, the org-wide alert list is empty for
  such a user, so an import planned a move to the folder the alert was already
  in, and a move made in the UI never showed as drift. The provider now checks
  the folder in state first, then lists each alert folder once per run, and
  warns when the alert is in none of them. Import also accepts
  `{org_id}/{folder_id}/{alert_id}`, and the `openobserve_alert` data source
  now reports `folder_id` for a lookup by ID.

  After upgrading, an alert whose configuration leaves `folder_id` unset (so
  it defaults to `"default"`) but which was moved to another folder in the UI
  will plan a move back to `default` on the first plan. This is the drift the
  provider could not see before. Set `folder_id` to the alert's folder to keep
  it where it is.

- **A pipeline destination created by 1.4.2 or earlier is now shown in the
  plan** ([#10](https://github.com/openobserve/terraform-provider-openobserve/issues/10)).
  Such a destination is stored as an alert destination, which a pipeline
  cannot send to. The new computed `module` attribute reads `alert` for one,
  and the plan shows an in-place update that converts it. An alert that also
  sends to that destination stops working once it is converted. The check
  runs only until state records `pipeline`, so a destination later re-saved as
  an alert destination outside Terraform is not detected again.

## [1.4.3] - 2026-11-05

### Fixed

- **Every `openobserve_pipeline` update after the first failed with HTTP 409**
  ([#6](https://github.com/openobserve/terraform-provider-openobserve/issues/6)).
  The update did not send the pipeline's version, which OpenObserve checks
  before accepting a change and bumps after each one:

  ```
  OpenObserve API error (HTTP 409): {"code":409,"message":"Pipeline with ID ... modified by someone else. Please refresh."}
  ```

  The update now sends the version recorded in state.

- **`openobserve_pipeline_destination` created an alert destination, so the
  pipeline dropped its records.** OpenObserve stores a destination as an alert
  destination unless create and update carry `module=pipeline`, and a
  pipeline's `remote_stream` node cannot send to an alert destination. The
  provider now sends `module=pipeline` on create and update
  ([#10](https://github.com/openobserve/terraform-provider-openobserve/issues/10)).
  A destination created by an earlier version is still stored as an alert
  destination, and a plan does not show it. The server converts it in place
  on the next update, so change any of its attributes and apply. Replacing
  only the destination fails, because the server refuses to delete a
  destination a pipeline still uses. `output_format` is not exposed yet.

- **A pipeline condition node written as a list failed every apply.**
  OpenObserve reads a legacy condition list back as `{"and": [...]}`, which it
  evaluates the same way, so `openobserve_pipeline` reported "Provider
  produced inconsistent result after apply" on `conditions`
  ([#11](https://github.com/openobserve/terraform-provider-openobserve/issues/11)).
  The two forms are now compared by meaning and state keeps the configured
  list. The same comparison serves alert `conditions`. A server change that
  alters the filter is still reported. Condition nodes still cannot use the v2
  condition format.

- **A pipeline reading a metrics or traces stream failed to apply.**
  `openobserve_pipeline.stream_type` defaulted to `logs`, but OpenObserve
  takes the pipeline source from the first node, so a pipeline whose source
  node reads a metrics stream came back as `metrics`
  ([#14](https://github.com/openobserve/terraform-provider-openobserve/issues/14)):

  ```
  Error: Provider produced inconsistent result after apply
  .stream_type: was cty.StringVal("logs"), but now cty.StringVal("metrics").
  ```

  When `stream_type` is not set it now comes from the first node's
  `stream_type`, and is still `logs` if that node does not set one. A
  pipeline-level `stream_name` or `stream_type` that disagrees with the first
  node is now rejected at plan time instead of failing after apply.

- **A JSON attribute not written in `json.Marshal` form failed to apply.**
  `dashboard_json` on `openobserve_dashboard`, `config` on
  `openobserve_synthetic` and `conditions` on `openobserve_alert` were stored
  re-marshalled (keys sorted, whitespace removed, `2.0` written as `2`, `&`
  escaped) even when the server's document matched the configuration, so a
  pretty-printed `file()` failed with "Provider produced inconsistent result
  after apply" and every later plan showed a diff
  ([#8](https://github.com/openobserve/terraform-provider-openobserve/issues/8)).
  The configured text is now stored as written. JSON keys no longer need to be
  in alphabetical order when the provider is used from Pulumi. A state written
  by an earlier version holds the re-marshalled text, so the first plan after
  upgrading can show one in-place update of the attribute; after that it stays
  clean.

## [1.4.2] - 2026-10-01

### Fixed

- **A stream could intermittently fail to apply with an inconsistent result.**
  OpenObserve can answer the read that follows a settings update with the
  settings from before it, because its settings cache is refreshed in the
  background
  ([openobserve/openobserve#14982](https://github.com/openobserve/openobserve/issues/14982)).
  Applying many streams at once made this easy to hit:

  ```
  Error: Provider produced inconsistent result after apply
  .enable_distinct_fields: was cty.True, but now cty.False.
  ```

  `openobserve_stream` now re-reads, with backoff for up to 15 seconds, until
  the server reports every configured value, and only then records state. A
  setting the server genuinely changed or rejected is still reported as
  before, just after that wait.

## [1.4.1] - 2026-09-10

### Added

- **README coverage for using this provider from Pulumi.** Pulumi bridges it
  directly with `pulumi package add terraform-provider openobserve/openobserve`,
  so there is no separate package to publish, but two things differ enough to
  cost an afternoon: keys inside a JSON string attribute stay snake_case while
  attribute names are camelCased, and those documents have to be written with
  their keys in alphabetical order or the resource shows a diff on every `up`.
  Both are documented, and the example was applied against a live server.

### Fixed

- **An alert or pipeline using a word-shaped operator could not be applied at
  all.** `conditions` accepts either `Contains` or `contains` on write and
  always answers snake_case, and the provider stored what came back. Where the
  configuration said `Contains`, that is an inconsistent result after apply,
  which Terraform refuses:

  ```
  Error: Provider produced inconsistent result after apply
  .query_condition.conditions: was ..."operator":"IsNotEmpty"...
                               but now ..."operator":"is_not_empty"...
  ```

  The read path now keeps the configured spelling, for `openobserve_alert` and
  for an `openobserve_pipeline` condition node, which carries the same
  document. This affected every release that supported these operators, not
  only `1.4.0`. On an affected version the workaround is to write the operator
  in snake_case, which round-trips.

- **`aggregation.having.value` is checked during `plan`.** It has to be present
  and parse as a number, whatever the operator, because an aggregate is a
  number. The schema said the opposite for unary operators, and following it
  earned `HTTP 400: aggregation threshold (having.value) is not numeric`, which
  names a field the configuration never mentions.

- **`trigger_condition.operator` rejects word-shaped operators during `plan`.**
  The schema allowed all twelve, but a threshold compares a row count to a
  number, so the server takes `Contains` and then fails while storing it with
  `HTTP 500: cannot convert contains into a trigger threshold operator`. A 500
  gives the user nothing to act on. The other operator attributes are
  unchanged.

### Notes

The `conditions` document is rewritten by the server in ways beyond operator
spelling: a bare array becomes `{"and": [...]}`, an unknown key alongside a
recognized one is dropped, and a document carrying **both** a top-level `or`
and a top-level `and` is stored with the `and` branch silently discarded.

Only the spelling is repaired. Restructuring changes the predicate rather than
its presentation, so it is left to surface as a failed apply instead of being
absorbed. Write one top-level key and nest beneath it. The alerting guide
covers this.

## [1.4.0] - 2026-09-03

Synthetic monitoring and ingestion tokens, plus alert changes that came out of
re-reading the server's alert models.

### Added

- **`openobserve_synthetic`**: checks that probe a target from outside your
  infrastructure. Five types, `http`, `tcp`, `tls`, `ssh` and `browser`, sharing
  a schedule, probe locations, retry policy and alert destinations. Type
  specific settings live in a `config` JSON document rather than five sets of
  modelled blocks, because the types share almost nothing; the provider compares
  it as JSON, so server side key reordering and defaults do not read as drift.
  Browser checks add `collect_rum_data` and `session_replay`, which are rejected
  during `plan` on any other type.

  Authentication is a flat `auth` block with a `type` discriminator rather than
  one block per kind. A nested block combined with `dynamic` produces a present
  object full of unknowns rather than a null, which makes any validation that
  tests for absence fire wrongly.

  Requires `ZO_SYNTHETICS_ENABLED=true`. When it is off the routes are not
  registered, so every path answers 404 and a missing check is indistinguishable
  from a disabled feature by status code alone. The provider tells the two apart
  and says which it is.

- **`openobserve_ingestion_token`**: credentials for collectors, agents and SDKs
  sending data in, as distinct from a service account, which authenticates calls
  to the management API. OpenObserve has no delete endpoint for these, so
  removing the resource disables the token and warns rather than reporting a
  deletion that did not happen. The record keeps its name, which means a name
  cannot be reused; the warning says so.

- **Three data sources**: `openobserve_synthetics` and
  `openobserve_synthetic_locations` for adopting checks and discovering where
  they can run, and `openobserve_ingestion_tokens` for auditing what is issued.
  The token data source deliberately reports names, descriptions and status but
  **not token values**: a data source that handed out live ingestion credentials
  to anything able to read the organization would be a poor default.

- **`pending_period_sec` on alerts and composite alerts.** The condition must
  hold for this long before the alert fires, which is how a single spiky
  evaluation is stopped from paging someone. Rejected on real time alerts, which
  evaluate per record and have no notion of a condition persisting.

- **Four unary comparison operators**: `IsNull`, `IsNotNull`, `IsEmpty` and
  `IsNotEmpty`, alongside the existing `Contains` and `NotContains`. These take
  no `value`, so `value` is now optional on a condition.

- **Guides for synthetic monitoring and ingestion tokens** on the Registry, and
  matching reference files plus a runnable example in the Claude skill.

### Fixed

- **Comparison operators are PascalCase.** The v2 alert API models them as an
  enum with exact names, so the snake_case spellings are rejected outright. The
  provider now pins the accepted set and an integration test asserts that
  snake_case is refused, rather than discovering it in a user's apply.

- **`group_by` on an alert no longer drifts.** The server derives it from the
  SQL when it is not set, so a configuration that omitted it planned a change on
  every run. It is now `Computed` as well as `Optional`.

- **Synthetic cookies and variables are read back.** They were written but never
  populated on read, so an imported check planned a spurious update that added
  them. As with `auth`, they are filled from the server only when the model has
  none, so a configured secret is never overwritten with the server's redaction
  of it.

- **`wait_before_retry_secs` no longer plans as unknown**, and defaults to 5 to
  match the server. It was the only retry attribute without a default, so an
  unset value went to `known after apply` and the provider then sent 0.

- **Empty strings from the server no longer read as configuration.** An
  optional field the server returns as `""` was written into state as `""` while
  a configuration that omitted it held null, so an imported synthetic planned an
  update that changed nothing.

### Notes

Two server behaviours that look like provider bugs and are not:

- A deleted stream holds its name until the data retention job clears the
  marker, on `ZO_COMPACT_DATA_RETENTION_INTERVAL` (3600 seconds by default).
  Until then recreating it fails with `stream [name] is being deleted`, so
  destroy-then-apply cycles fail on streams.
- A synthetic check needs a folder with `folder_type = "synthetics"`. An
  `alerts` folder fails with an opaque `FOREIGN KEY constraint failed (787)`.
  This cuts against SLOs, which live in alert folders for want of a type of
  their own. Both are documented in the guides and the skill's error reference.

Every resource, data source and example in the repository and the skill was
applied against a live server before release, including the full 28 resource
configuration, its idempotency and import round trips, and 20 integration tests.

## [1.3.1] - 2026-08-25

Documentation only. No provider behaviour changed, and the resource and data
source schemas are identical to `1.3.0`.

### Added

- **A pipelines guide on the Registry.** `1.3.0` shipped the resources without
  one, so the only narrative coverage lived in the repository's Claude skill,
  which Registry readers never see. The guide covers the graph model, the four
  node types, what the provider fills in (`io_type`, positions, edge ids), VRL
  functions and their trailing dot, condition nodes, the one-realtime-pipeline
  per-source-stream rule, and finding the server-assigned id an import needs.
- **The alerting guide now points pipeline destinations at their own resource.**
  It already noted that a destination without a template cannot be used by an
  alert; it now says which resource to reach for instead, and why the two are
  separate.

### Changed

- The provider index and getting-started guide list pipelines alongside the
  other areas.
- The Claude skill under `skills/` is current with `1.3.0`: every example pins
  `~> 1.3`, the complete-stack example now includes a pipeline so it is actually
  complete, and the import and drift reference covers the new resources' import
  IDs, the function adoption path, and VRL body spelling preservation.

Every example in the repository and the skill was applied against a live server
before release.

## [1.3.0] - 2026-08-24

### Added

- **`openobserve_pipeline`**: pipelines that transform records in flight.
  A pipeline is a graph, so it is written as `node` blocks joined by `edge`
  blocks: records enter from a source stream, pass through nodes that transform
  or filter them, and land in another stream or an external endpoint. Four node
  types are supported: `stream`, `function`, `condition` and `remote_stream`.
- **`openobserve_function`**: VRL and JavaScript transforms. The server compiles
  the body when it is saved, so a syntax error fails `terraform apply` rather
  than surfacing later as a pipeline that quietly stopped working.
- **`openobserve_pipeline_destination`**: an external endpoint a pipeline
  forwards to. This is the same underlying object as an alert destination, and
  the server tells the two apart by one field: a destination carrying a template
  is for alerts, one without is for pipelines. They are separate resources here
  because that field decides what the object is for, and because templates,
  email recipients and SNS do not apply to a pipeline.
- **`openobserve_function`, `openobserve_functions` and `openobserve_pipelines`
  data sources.** The singular function data source reports `used_by`, the
  pipelines holding it, which is what answers why a delete was refused.
  `openobserve_pipelines` carries the server-assigned identifiers a
  `terraform import` needs, since a pipeline's id appears nowhere in
  configuration.

### Ordering

A pipeline's function and destination must be **referenced**, not named:

```hcl
function_name = openobserve_function.redact.name   # not "redact"
```

The server refuses to delete either while a pipeline still uses it, answering
`409` with the dependent pipelines listed. That reference is what tells
Terraform to create them first and destroy the pipeline first; a literal string
produces a teardown that fails. Both refusals are annotated with what to do
about them.

### What the provider fills in

Three things the pipeline API requires but nobody should have to write:

- `io_type` is inferred from the edges: nothing arriving means `input`, nothing
  leaving means `output`, anything else is `default`.
- Node positions are laid out left to right in declaration order. They are
  cosmetic, but the server rejects a node without them.
- Edge ids follow the server's own `e{from}-{to}` convention, so a pipeline
  written in Terraform looks like one built in the UI.

All three can still be set explicitly.

### Normalization handled

Two places where the server does not store what it was sent, both of which would
otherwise show as drift on every plan:

- A VRL body gains a trailing `.`, because a VRL program's value is its last
  expression and a transform has to return the record.
- A condition node's JSON comes back with keys in struct order, while
  `jsonencode()` sorts them alphabetically.

## [1.2.1] - 2026-08-24

Bug fixes only. The resource and data source schemas are unchanged apart from
one new computed attribute, so upgrading from `1.2.0` needs no configuration
changes.

### Fixed

- **Destroying an `openobserve_stream` silently left it in place when the server
  had renamed it**
  ([#1](https://github.com/openobserve/terraform-provider-openobserve/issues/1)).
  OpenObserve replaces every character outside `[a-zA-Z0-9_:]` in a stream name
  with an underscore, so a stream created as `live-test-stream` is stored as
  `live_test_stream`. Create, schema, settings and update all normalize the name
  they are given, so the configured name kept working everywhere the provider
  looked. The delete endpoint does not normalize, so it answered 404 for a stream
  that plainly existed, and the provider tolerated that 404 as "already gone".
  The result reported success, dropped the resource from state, and left the
  stream and its data behind. Deletes now resolve the stored name through the
  normalizing read path first, so a 404 is only tolerated once a read has
  confirmed the stream really is absent.
- **`openobserve_stream` gained `effective_name`**, reporting the name
  OpenObserve actually stored. Alerts, dashboards and SQL have to reference that
  name, so a rename the provider hid was a problem well beyond deletion.
- **`terraform plan` failed outright on a variable-driven `multi_time_range`**
  ([#2](https://github.com/openobserve/terraform-provider-openobserve/issues/2)).
  The block was modelled as a Go slice, which cannot hold an unknown value, so a
  `dynamic "multi_time_range"` block whose `for_each` came from a variable failed
  config decode with *Received unknown value, however the target type cannot
  handle unknown values*, before any validation ran. A literal `for_each` worked,
  which made this look like a problem with the block's content rather than with
  its type. It is now a framework list.
- **A module wrapping `openobserve_slo` reported conflicting indicators that
  were never set**
  ([#3](https://github.com/openobserve/terraform-provider-openobserve/issues/3)).
  Terraform materializes a `SingleNestedBlock` whose `dynamic` produced no
  iterations as a present object full of unknowns rather than as a null object,
  so the provider's nil check counted indicators nobody wrote. Validation now
  reads the raw configuration and tells absent apart from not-yet-known,
  deciding only when the answer is certain.

### Changed

- **Validation no longer fires on unknown values.** The report above was one
  instance of a general rule the provider broke: several checks tested
  `!IsNull()`, which is also true of a value that has simply not resolved yet.
  That produced diagnostics nobody could act on, because the configuration was
  correct and only evaluation order made the value unavailable. The alert
  resource's count-gate, burn-rate window, aggregation warning and cron checks
  were all reachable this way from a module, and are now guarded.

## [1.2.0] - 2026-08-20

### Added

- **`openobserve_composite_alert`**: alerts that fire on a boolean expression
  over other alerts' current states rather than on a query of their own. "The
  error rate is high" and "a deploy went out recently" are separate alerts;
  paging only on both is a composite of them, with no duplicated query and no
  second evaluation cost. Supports `&&`, `||` and `!` with the usual precedence,
  between 2 and 10 children, and composites as children one level deep.
  Real-time alerts are not eligible children, having no scheduled state to read.
- **`warning_counts_as_firing` and `stale_child_policy`** decide what a child
  contributes: whether a child at warning counts as firing, and whether a child
  whose state has gone stale keeps its last value, stops satisfying the
  expression, or satisfies it. The last is the fail-safe choice for
  absence-of-heartbeat patterns.
- **`openobserve_composite_alert` data source**: the composite's configuration
  together with the state it actually read from each child, including per-child
  `stale` and `truth`, and its own last `evaluation`. `evaluation` is null until
  the composite has run once, which is not the same as having evaluated false.
- **`openobserve_composite_alert_references` data source**: the composites that
  hold a given alert as a child. The server refuses to delete a referenced
  alert, so this answers why a destroy was blocked, including for composites
  created outside Terraform. `hidden_reference_count` reports references
  permissions hid, so an empty list is not read as "unreferenced".
- **Plan-time expression checking**: syntax, operand count, and duplicate
  operands are reported as a diagnostic on the offending line rather than as an
  apply-time HTTP 400. The provider parses expressions with the same grammar and
  precedence as the server, and an integration test asserts the two still agree
  on the canonical form.

### Fixed

- **`ignore_case` on alert conditions was missing.** The server's condition
  model carries `column`, `operator`, `value` and `ignore_case`; the provider
  modelled only the first three, so case-insensitive string comparisons could
  not be expressed and were silently dropped.
- **Composite errors are annotated rather than passed through.** A blocked
  delete now explains the ordering that caused it, and `composite_too_deep`,
  `composite_cycle`, `child_not_eligible`, `composite_folder_not_found` and the
  writes-disabled and super-cluster cases each say what to do about it.
- **Release checksums omitted the Terraform Registry manifest.** The manifest
  was uploaded as a release asset but never listed in the signed `SHA256SUMS`,
  so the Registry refused to ingest a version with *missing SHA256 checksum for
  \[..._manifest.json\]*. `checksum.extra_files` now covers it, matching the
  `release.extra_files` entry that was already there. A
  `repair-release-checksums` workflow fixes releases published before this.

## [1.1.0] - 2026-08-17

### Added

- **`openobserve_slo`**: service level objectives. Three indicator types, each
  answering a different question: `count_sli` (good events over total, with
  single-query, dual-query, and PromQL sources), `time_slice_sli` (each slice
  good or bad against a threshold, including `absent_is_bad` for pipeline
  freshness), and `alert_sli` (derived from an alert's firing state). Supports
  grouping, tags, pausing, and moving between folders. SLOs live in alert
  folders; there is no separate SLO folder type.
- **`openobserve_slo` and `openobserve_slos` data sources**: an objective's
  definition together with its current measurement: coverage, SLI, error budget
  remaining, burn rate, and time to exhaustion. `status` is null until the first
  evaluation pass, and `no_data` marks the frozen state where an objective is
  neither healthy nor breached, so neither is reported as zero.
- **SLO alerts**: `query_condition` gained the `slo` type and a `slo_condition`
  block, firing on error budget consumed or on burn rate across two windows.
- **PromQL per-group alerting**: `promql_multi_alert` evaluates and notifies per
  returned series rather than collapsing the query to a single verdict,
  completing per-group support across the alert families.
- **`promql_warning_value`**: a warning level for PromQL alerts, matching the
  warning thresholds the other families already had.
- **Alert deduplication**: a `deduplication` block collapsing repeated firings
  of the same underlying issue, with explicit or inferred fingerprint fields.
- `row_template_type`, `creates_incident`, and `workflows` on `openobserve_alert`.
- **Documentation guides** rendered into the Terraform Registry: getting
  started, alerting, service level objectives, dashboards, and roles and groups.

### Fixed

- **The dashboard example documented a panel filter shape the API rejects.** A
  list-shaped `filter` is refused with *data did not match any variant of
  untagged enum PanelFilter*; it must be an object, and an empty group is the
  "no filter" case. The corrected example is applied against a live server as
  part of the test suite rather than written from the type definitions.

### Validated before apply

The provider now reports these during `plan`, instead of letting the server
reject the apply:

- An SLO's `window_secs` must be 7, 30, or 90 days, and `target` must fall
  strictly inside (0, 100).
- Exactly one SLO indicator block, and exactly one `count_sli` source.
- `absent_is_bad` cannot be combined with `group_by`: gap fill cannot observe a
  group missing from an entire pass, so such an objective would freeze rather
  than fire for the very failure it watches.
- A burn-rate alert needs both windows. The short one is deliberately not
  derived from the long one, because the minimum is twice the SLO's slice interval, and
  that interval belongs to the objective, so a guessed value could be rejected.
- An SLO alert must not set `trigger_condition.threshold` or `operator`: that
  family has no count gate, and its thresholds live on `slo_condition`.
- `trigger_condition.warning_threshold` is not accepted on aggregation alerts,
  where the count threshold is coverage rather than severity; the warning level
  belongs on `aggregation.warning_value`.

### Known limitations

- Per-group SLO alerting (`slo_condition.multi_alert`) is not yet supported by
  OpenObserve and is rejected server-side. The attribute is present so it starts
  working the moment the server does. Per-group alerting works today on
  aggregation and PromQL alerts.

## [1.0.1] - 2026-08-15

Packaging and licensing corrections. No provider behaviour changed; the
resource and data source schemas are identical to `1.0.0`.

### Fixed

- **The repository was not recognised as Apache-2.0 licensed.** `LICENSE` held a
  modified copy of the Apache-2.0 text, differing from the canonical wording on
  135 lines. The definitions of "Work" and "Contribution" had been rewritten,
  along with clauses in the patent-litigation and liability sections. GitHub
  therefore classified the repository as `NOASSERTION`, and the OpenTofu
  registry refused to serve the provider's documentation with *451 Unavailable
  for Legal Reasons*. `LICENSE` is now the verbatim Apache-2.0 text, and the
  copyright notice that had been edited into it moved to `NOTICE`.
- **The Terraform Registry recorded the wrong plugin protocol.** The Registry
  reads supported protocol versions from a `..._manifest.json` release asset
  that was never published, so it fell back to its default of protocol 5.0 for
  every released version, while this provider is built on
  terraform-plugin-framework and speaks only 6.0. Terraform 1.x negotiates the
  real protocol during the plugin handshake, so installs were unaffected;
  Terraform 0.12 through 0.15 would have been told the provider was compatible
  and then failed to launch it. The manifest now ships with each release.
- **Releases would have started failing without warning.** `.goreleaser.yml`
  used `archives.format`, removed in GoReleaser v2, while the release workflow
  tracks the latest v2.
- **The documentation check could never agree with the command it recommended.**
  CI ran a bare `tfplugindocs generate` while `make docs` passes
  `--provider-name`; without that flag the provider name is derived from the
  checkout directory, which differs between CI and a local clone. Both now go
  through `make docs`.

## [1.0.0] - 2026-08-14

First release with a stability commitment. The `0.0.x` line was a preview: three
of its resources did not work against the API at all, and fixing them required
schema changes that a `0.x` series is free to make and a `1.x` series is not.
Everything here is now exercised against a live OpenObserve, open-source and
Enterprise alike, so the surface is worth holding still.

From this release on, the resource and data source schemas follow semantic
versioning: attributes will not be removed or repurposed without a `2.0.0`.

### Upgrading from 0.0.x

Two resources changed shape. Neither previously worked, so a configuration that
was actually applying is unlikely to be affected.

- `openobserve_stream` settings moved out of the nested `settings` block to
  top-level attributes, and `partition_keys` became an attribute:

  ```hcl
  # 0.0.x                          # 1.0.0
  settings {                       data_retention        = 30
    data_retention = 30            full_text_search_keys = ["message"]
    full_text_search_keys = [...]  partition_keys = [
  }                                  { field = "service", type = "value" },
                                   ]
  ```

- `openobserve_dashboard` takes the dashboard document in `dashboard_json`
  instead of separate `title`, `panels`, and `variables` attributes. Export the
  dashboard from the UI, or read it from the `openobserve_dashboard` data
  source, and pass it through `jsonencode()` or `file()`.

Existing resources can be adopted rather than recreated: every resource supports
`terraform import`, so `terraform state rm` followed by `terraform import` moves
a stream or dashboard onto the new schema without touching the server.

### Added

#### Resources
- **`openobserve_organization`**: create and rename organizations. OpenObserve exposes no organization delete API, so destroying the resource removes it from state and warns.
- **`openobserve_folder`**: folders for dashboards, alerts, reports, and synthetics.
- **`openobserve_service_account`**: service accounts with API token issuance and rotation through a `rotate_token` trigger. The token is only returned on creation and rotation, so it is stored in state and marked sensitive.
- **`openobserve_role`**: custom roles and their permissions, expressed as `{object, permission}` pairs against a resource type (`stream`) or a single entity (`stream:my_logs`). Enterprise only.
- **`openobserve_group`**: user groups and the custom roles they grant. Enterprise only.
- **`openobserve_alert_template`**: notification message templates for the `http`, `email`, and `sns` channels.
- **`openobserve_alert_destination`**: webhook, email, and SNS destinations, with plan-time validation of the fields each type requires.
- **`openobserve_alert`**: scheduled and real-time alerts covering SQL, PromQL, and aggregation queries, warning thresholds, cron scheduling, priority, and tags.

#### Data Sources
- `openobserve_organizations`, `openobserve_streams`, `openobserve_user`, `openobserve_users`, `openobserve_user_roles`, `openobserve_service_accounts`, `openobserve_role`, `openobserve_roles`, `openobserve_group`, `openobserve_groups`, `openobserve_resources`, `openobserve_folder`, `openobserve_folders`, `openobserve_dashboard`, `openobserve_dashboards`, `openobserve_alert_template`, `openobserve_alert_templates`, `openobserve_alert_destination`, `openobserve_alert_destinations`, `openobserve_alert`, `openobserve_alerts`

#### Provider
- `org_id` is now optional on every resource and data source, falling back to the provider-level `org_id`.
- Endpoints that exist only in OpenObserve Enterprise now report that fact instead of surfacing a bare HTTP 403.
- Integration test suite covering every client call, including deletes, against a live OpenObserve instance. It skips unless `OPENOBSERVE_ENDPOINT` is set.
- Schema validation tests that check every resource and data source schema is a valid implementation.

### Fixed

- **`openobserve_stream` settings were never applied.** The settings endpoint takes each list setting as an `{add, remove}` delta, but the provider sent absolute arrays, which the server rejected. Settings are now diffed against the server's current state and pushed as deltas.
- **`openobserve_stream` could not read a stream back on many server versions.** Partition keys come back as a level-keyed map (`{"L0": {…}}`) on some builds and as an array on others, and distinct value fields vary between objects and bare strings. Both shapes are now decoded.
- **`openobserve_stream` reported permanent drift on `index_fields`.** OpenObserve registers every bloom filter field as a secondary index field, making the server's list a superset of the configured one. A superset is no longer treated as drift, and retired partition keys no longer appear as spurious list entries.
- **`openobserve_stream` never created streams.** The resource only pushed settings, so applying it to a stream that did not exist failed. It now creates the stream and adopts one that already exists.
- **`openobserve_dashboard` sent a body the API rejects** and dropped the server-assigned `dashboardId` on update, which created a duplicate dashboard instead of updating the existing one. The document now round-trips correctly, the concurrency `hash` is refreshed before each write, and fields the server adds to the document no longer show up as drift.
- **`openobserve_dashboard` ignored folders.** Dashboards can now be created in a folder and moved between folders, and the folder is resolved on read and on import.
- **`openobserve_user` could not manage a user who already existed** in another organization; that case is now treated as granting membership. A user who is already a member of the target organization answers HTTP 409, which is now treated as adoption rather than an error, and the configured role and custom roles are applied to the adopted account. Passwords are sent only when they actually change, and `custom_roles` are supported.
- The built-in roles endpoint returns `{label, value}` objects, not strings; `openobserve_user_roles` decodes both shapes.
- **`openobserve_group` silently dropped every role on creation.** The create endpoint only seeds members and ignores `roles` entirely, so roles are now always applied through the update endpoint afterwards.
- **`openobserve_group` created groups that OpenObserve would not list.** The server records the group-to-organization link only when the create request carries no users; creating a group with members writes just the membership links, leaving the group invisible to the group list endpoint and to the UI. Groups are now created empty and populated immediately afterwards, which also repairs a group previously created the other way.
- **`openobserve_alert` ignored `folder_id` changes.** The update endpoint deliberately leaves an alert's folder alone, so moving an alert now goes through the dedicated move endpoint.
- **`openobserve_role` permissions could not express a grant over a whole resource type.** OpenObserve stores such a grant against an organization-scoped wildcard entity (`stream:_all_myorg`). A bare resource type (`stream`) is now accepted as shorthand for it, expanded on write, and kept in whichever spelling was configured so neither form reads back as drift.
- **`openobserve_role` and `openobserve_group` accepted names the server silently rewrites.** Any character outside `[a-zA-Z0-9_]` becomes an underscore, so `payments-owner` was stored as `payments_owner` and Terraform then tracked a name that did not exist. Such names are now rejected during planning.

### Changed

- **Breaking:** `openobserve_stream` settings moved from a nested `settings` block to top-level attributes, and `partition_keys` is an attribute (`partition_keys = [{ … }]`) rather than a block. The old block never worked against the API, so no working configuration is affected.
- **Breaking:** `openobserve_dashboard` takes the dashboard document in `dashboard_json` rather than separate `title`, `panels`, and `variables` attributes. This keeps the resource faithful to every dashboard schema version instead of modelling one of them.

## [0.0.1] - 2024-04-30

### Added

#### Provider
- OpenObserve Terraform provider using the [Terraform Plugin Framework](https://github.com/hashicorp/terraform-plugin-framework) (protocol v6)
- Basic auth via explicit provider arguments or environment variables (`OPENOBSERVE_ENDPOINT`, `OPENOBSERVE_USERNAME`, `OPENOBSERVE_PASSWORD`, `OPENOBSERVE_ORG_ID`)
- Publishes to the Terraform Registry as `openobserve/openobserve`

#### Resources
- **`openobserve_stream`**: manage stream settings (retention, full-text search keys, index fields, bloom-filter fields, partition keys) for `logs`, `metrics`, and `traces` streams; `terraform import` support with `{org_id}/{stream_type}/{name}`
- **`openobserve_dashboard`**: create, update, and delete dashboards with a full JSON `definition` field that captures panels, variables, and layout; `terraform import` support with `{org_id}/{dashboard_id}`
- **`openobserve_user`**: manage users within an organization including role (`admin`, `editor`, `viewer`) and optional password; `terraform import` support with `{org_id}/{email}`

#### Data Sources
- **`openobserve_stream`**: read stream settings and storage type for an existing stream
- **`openobserve_organization`**: look up organization metadata by identifier

#### Repository
- GoReleaser v2 build pipeline with multi-platform binaries, SHA256 checksums, and GPG signing
- GitHub Actions CI (build, vet, unit tests, golangci-lint, docs diff check)
- GitHub Actions release workflow triggered on version tags
- Comprehensive examples for all resources and data sources
- Apache 2.0 license

[Unreleased]: https://github.com/openobserve/terraform-provider-openobserve/compare/v1.5.0...HEAD
[1.5.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.5.0
[1.4.3]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.4.3
[1.4.2]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.4.2
[1.4.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.4.1
[1.4.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.4.0
[1.3.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.3.1
[1.3.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.3.0
[1.2.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.2.1
[1.2.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.2.0
[1.1.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.1.0
[1.0.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.0.1
[1.0.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.0.0
[0.0.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v0.0.1
