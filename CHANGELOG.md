# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] — 2026-08-14

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
- **`openobserve_organization`** — create and rename organizations. OpenObserve exposes no organization delete API, so destroying the resource removes it from state and warns.
- **`openobserve_folder`** — folders for dashboards, alerts, reports, and synthetics.
- **`openobserve_service_account`** — service accounts with API token issuance and rotation through a `rotate_token` trigger. The token is only returned on creation and rotation, so it is stored in state and marked sensitive.
- **`openobserve_role`** — custom roles and their permissions, expressed as `{object, permission}` pairs against a resource type (`stream`) or a single entity (`stream:my_logs`). Enterprise only.
- **`openobserve_group`** — user groups and the custom roles they grant. Enterprise only.
- **`openobserve_alert_template`** — notification message templates for the `http`, `email`, and `sns` channels.
- **`openobserve_alert_destination`** — webhook, email, and SNS destinations, with plan-time validation of the fields each type requires.
- **`openobserve_alert`** — scheduled and real-time alerts covering SQL, PromQL, and aggregation queries, warning thresholds, cron scheduling, priority, and tags.

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

## [0.0.1] — 2024-04-30

### Added

#### Provider
- OpenObserve Terraform provider using the [Terraform Plugin Framework](https://github.com/hashicorp/terraform-plugin-framework) (protocol v6)
- Basic auth via explicit provider arguments or environment variables (`OPENOBSERVE_ENDPOINT`, `OPENOBSERVE_USERNAME`, `OPENOBSERVE_PASSWORD`, `OPENOBSERVE_ORG_ID`)
- Publishes to the Terraform Registry as `openobserve/openobserve`

#### Resources
- **`openobserve_stream`** — manage stream settings (retention, full-text search keys, index fields, bloom-filter fields, partition keys) for `logs`, `metrics`, and `traces` streams; `terraform import` support with `{org_id}/{stream_type}/{name}`
- **`openobserve_dashboard`** — create, update, and delete dashboards with a full JSON `definition` field that captures panels, variables, and layout; `terraform import` support with `{org_id}/{dashboard_id}`
- **`openobserve_user`** — manage users within an organization including role (`admin`, `editor`, `viewer`) and optional password; `terraform import` support with `{org_id}/{email}`

#### Data Sources
- **`openobserve_stream`** — read stream settings and storage type for an existing stream
- **`openobserve_organization`** — look up organization metadata by identifier

#### Repository
- GoReleaser v2 build pipeline with multi-platform binaries, SHA256 checksums, and GPG signing
- GitHub Actions CI (build, vet, unit tests, golangci-lint, docs diff check)
- GitHub Actions release workflow triggered on version tags
- Comprehensive examples for all resources and data sources
- Apache 2.0 license

[1.0.0]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v1.0.0
[0.0.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v0.0.1
