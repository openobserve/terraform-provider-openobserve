# Server errors, decoded

Every message here was produced by a live OpenObserve server. Match on the
distinctive fragment; the surrounding text varies by version.

## Streams

| Message | Cause | Fix |
|---|---|---|
| `stream already exists` | Creating a stream that ingestion already created | None. The provider adopts it |
| `stream [x] is being deleted` | Retried destroy; deletion is asynchronous | None. Treated as success |
| `partition key [x] cannot also be a secondary index field` | Field in both lists | Keep `partition_keys` and `index_fields` disjoint |
| `stream settings could not be found` | Settings PUT against a stream that does not exist | Create the stream first |
| `Stream <name> not found` (404) | Creating an alert on a stream that does not exist | Declare the stream, or wait for ingestion to create it |

## Dashboards

| Message | Cause | Fix |
|---|---|---|
| `data did not match any variant of untagged enum PanelFilter` | Panel `filter` shaped as a list | Use `{filterType="group", logicalOperator="AND", conditions=[]}` |
| A second dashboard appears instead of an update | `dashboardId` missing from the body | The provider injects it; only affects raw API calls |

## Alerts

| Message | Cause | Fix |
|---|---|---|
| `Alert destination or workflows is required` | Alert with neither | Add a destination |
| `warning_threshold is not supported on aggregation alerts` | Warning set on the count gate | Use `aggregation.warning_value` |
| `aggregation threshold (having.value) is not numeric` | String `having.value` | An aggregation aggregates numbers; use a numeric threshold |
| `unknown variant ''` | Empty enum string serialized | Send a valid variant, for example `operator = "="` |
| `{"code":400,"message":"4 /10 * * * *\n ^\n"}` | A five-field cron. The server rewrote the leading `*` to the current second | Cron takes SIX fields, seconds first: `0 */10 * * * *` |
| A folder change silently does nothing | `update_alert` passes `None` for the folder | The provider issues a separate move; only affects raw API calls |

## SLOs

| Message | Cause | Fix |
|---|---|---|
| `window 86400s is not one of the supported rolling windows (7d, 30d, 90d)` | Arbitrary SLO window | Use 604800, 2592000 or 7776000 |
| `missing field 'source'` | Count SLI config without the `source` wrapper | Nest `{mode, query}` under `source` |
| `good_query has an invalid projection: histogram() needs a quoted interval literal` | `histogram(_timestamp)` in an SLO dual query | Write `histogram(_timestamp, '5 minute')` |

## SLO alerts

| Message | Cause | Fix |
|---|---|---|
| `SLO alerts have no count gate` | `trigger_condition.threshold` or `operator` set | Omit both entirely |
| `burn-rate alerts require both a long and a short window` | Only one window given | Set both explicitly |
| `short window 300s must be at least 600s (2 slices)` | Short window under two slice intervals | Raise it. The minimum depends on the SLO's `slice_interval_secs` |
| `per-group alerting (multi_alert) is not yet supported for SLO alerts` | `slo_condition.multi_alert = true` | Alert on the rollup. Works today on aggregation and PromQL |

## Composite alerts

These arrive as machine-readable `code` values in the JSON body. The provider
appends an explanation to each.

| Code | Cause | Fix |
|---|---|---|
| `composite_unsupported_field` | A scheduling or query field sent on a composite | Remove it. Only `silence` is accepted |
| `composite_invalid_expression` | Syntax, operand count, or a duplicate operand | The provider catches these during `plan` |
| `child_not_eligible` | A real-time alert used as a composite child | Real-time alerts have no scheduled state to read |
| `child_referenced` (409) | Deleting an alert a composite still names | Destroy the composite first. Interpolate `alert_id` so Terraform orders it |
| `composite_cycle` (409) | Composite references form a loop | References must form a tree |
| `composite_too_deep` (409) | A composite of a composite of a composite | Nesting is capped at depth 2 |
| `composite_folder_not_found` | Folder does not exist | Composites do not create folders on demand |
| `composite_graph_lock_unavailable` | Concurrent composite writes in one organization | Apply with `-parallelism=1` |
| `composite_writes_disabled` | `ZO_ALERT_COMPOSITE_WRITES_ENABLED=false` | Re-enable it. It is on by default |
| `composite_super_cluster_unsupported` | Composites on a super-cluster deployment | Not available there |

## IAM

| Message | Cause | Fix |
|---|---|---|
| `Not Supported` (403) | Enterprise endpoint on open source | Enable Enterprise with `ZO_OPENFGA_ENABLED=true` |
| `User is already part of the organization` (409) | User already a member | None. The provider adopts |
| A role or group name comes back changed | Server rewrites `[^a-zA-Z0-9_]` to `_` | The provider blocks these during `plan`. Use underscores |
| A group is invisible in the UI | Created through the raw API **with** members, so the org link was never written | Import and apply; the provider creates empty then populates |

## Provider-side errors

These come from the provider during `plan`, before any request is made.

| Message fragment | Meaning |
|---|---|
| `Invalid composite expression` | Syntax, fewer than 2 or more than 10 children, or a duplicate operand |
| `a composite needs at least 2 children` | A composite of one is just the child |
| `child ... referenced more than once` | Duplicates are rejected, not deduplicated |
| `Provider produced inconsistent result after apply` | A provider bug. Report it with the resource type and the attribute named |

## Not an API error

**SQLite `code: 517`** is write contention on a local single-node metastore,
seen when Terraform applies many resources in parallel against a dev server. Use
`terraform apply -parallelism=1`.

**`401 Unauthorized` on every request** usually means `OPENOBSERVE_USERNAME` is
set to a username rather than the login email. Authentication is HTTP Basic with
the email address.

## Reading an unfamiliar error

1. If the body has a `"code"` field with a snake_case string, it is a composite
   alert error; see the table above.
2. If it mentions `Failed to deserialize the JSON body into the target type`,
   the shape is wrong rather than the values. For SLOs that is almost always the
   `source` wrapper; for dashboards it is almost always `filter`.
3. If it is a bare 404 naming a stream, the stream does not exist yet.
4. If it is a 403 saying `Not Supported`, the endpoint is Enterprise-only.
