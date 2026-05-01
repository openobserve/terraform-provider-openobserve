# Changelog

All notable changes to this project will be documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[0.0.1]: https://github.com/openobserve/terraform-provider-openobserve/releases/tag/v0.0.1
