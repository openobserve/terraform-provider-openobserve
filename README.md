# Terraform Provider for OpenObserve

[![CI](https://github.com/openobserve/terraform-provider-openobserve/actions/workflows/ci.yml/badge.svg)](https://github.com/openobserve/terraform-provider-openobserve/actions/workflows/ci.yml)
[![Registry](https://img.shields.io/badge/Terraform_Registry-openobserve%2Fopenobserve-623CE4?logo=terraform)](https://registry.terraform.io/providers/openobserve/openobserve/latest)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Manage [OpenObserve](https://openobserve.ai) with Terraform, OpenTofu, or
Pulumi: organizations, streams, dashboards, alerting, service level objectives,
synthetic monitoring, pipelines, and IAM.

## Requirements

| Tool       | Version |
|------------|---------|
| Terraform  | >= 1.0  |
| OpenObserve| >= 0.14 |
| Go         | >= 1.25 (development only) |

## Quick Start

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

resource "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs"

  data_retention        = 30
  full_text_search_keys = ["message"]
  index_fields          = ["level"]

  partition_keys = [
    { field = "service", type = "value" },
  ]
}

resource "openobserve_alert" "high_error_rate" {
  name         = "high-error-rate"
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(*) AS total FROM \"app_logs\" WHERE level = 'error'"
  }

  trigger_condition {
    period    = 15
    operator  = ">="
    threshold = 100
    frequency = 5
    silence   = 60
  }
}
```

Every resource takes an optional `org_id`. Leave it out and the provider's `org_id`
is used, so a single-organization setup never has to repeat it.

## Authentication

Pass credentials via environment variables to keep them out of the configuration:

```bash
export OPENOBSERVE_ENDPOINT="https://openobserve.example.com"
export OPENOBSERVE_USERNAME="admin@example.com"
export OPENOBSERVE_PASSWORD="your-password"
export OPENOBSERVE_ORG_ID="default"
```

## Using this provider from Pulumi

There is no separate `pulumi-openobserve` package. Pulumi bridges this provider
directly, so you get every resource and data source listed below without
anything extra to install:

```bash
pulumi package add terraform-provider openobserve/openobserve
```

That pulls the published provider from the Terraform Registry, generates an SDK
for your project's language, and installs it. Verified with Pulumi 3.261. In
TypeScript:

```ts
import * as openobserve from "@pulumi/openobserve";

const provider = new openobserve.Provider("oo", {
  endpoint: "https://openobserve.example.com",
  username: "admin@example.com",
  password: process.env.OO_PASSWORD,
  orgId: "default",
});

const folder = new openobserve.Folder("alerts", {
  name: "Reliability",
  folderType: "alerts",
}, { provider });

const stream = new openobserve.Stream("appLogs", {
  name: "app_logs",
  streamType: "logs",
  dataRetention: 30,
  fullTextSearchKeys: ["message"],
}, { provider });

const locations = openobserve.getSyntheticLocationsOutput({}, { provider });
```

### What changes when you cross the bridge

| Terraform | Pulumi |
|---|---|
| `folder_type`, `org_id` | `folderType`, `orgId` (attributes are camelCased) |
| repeated blocks: `cookie`, `variable` | pluralized arrays: `cookies`, `variables` |
| single blocks: `auth`, `query_condition` | stay singular: `auth`, `queryCondition` |
| `data "openobserve_alerts"` | `getAlerts()` / `getAlertsOutput()` |

Sensitive attributes stay sensitive, and provider warnings come through intact,
including the one explaining that removing an `openobserve_ingestion_token`
disables it rather than deleting it.

### Two things that will bite you

**JSON string attributes keep their server-side spelling.** Attribute *names*
are camelCased, but attributes that carry a JSON document (`conditions` on an
alert, `config` on a synthetic, a dashboard's JSON) are opaque strings passed
straight to the API. The keys inside them are **not** camelCased:

```ts
// right: keys inside the JSON stay snake_case
conditions: JSON.stringify({
  or: [{ column: "level", operator: "Contains", value: "error", ignore_case: false }],
}),
```

Writing `ignoreCase` there produces an HTTP 422 that names neither the field nor
the cause.

**Write JSON keys in alphabetical order.** The provider stores these documents
key-sorted. HCL's `jsonencode` also sorts, so Terraform matches; JavaScript's
`JSON.stringify` preserves insertion order, so it does not, and the resource
shows a diff on every `pulumi up` forever:

```ts
// churns on every up
config: JSON.stringify({ method: "GET", expect_status: 200, timeout_ms: 10000 }),

// stable
config: JSON.stringify({ expect_status: 200, method: "GET", timeout_ms: 10000 }),
```

Also worth knowing: the generated SDK types every input as optional, including
attributes this provider marks required, so a missing `name` surfaces at `pulumi
up` rather than at compile time.

## Resources

| Name                              | Description                                                              |
|-----------------------------------|--------------------------------------------------------------------------|
| `openobserve_organization`        | Organizations (create and rename; OpenObserve has no delete API)         |
| `openobserve_stream`              | Streams: retention, partitioning, indexing, schema options               |
| `openobserve_folder`              | Folders for dashboards, alerts, reports, and synthetics                  |
| `openobserve_dashboard`           | Dashboards from a JSON document, any schema version                      |
| `openobserve_user`                | Users and their membership of an organization                            |
| `openobserve_service_account`     | Service accounts with API tokens and rotation                            |
| `openobserve_role` †              | Custom roles and their permissions                                       |
| `openobserve_group` †             | User groups and the roles they grant                                     |
| `openobserve_alert_template`      | Notification message templates                                           |
| `openobserve_alert_destination`   | Webhook, email, and SNS destinations                                     |
| `openobserve_alert`               | Scheduled and real-time alerts (SQL, PromQL, aggregation, and SLO)      |
| `openobserve_composite_alert`     | Alerts that combine other alerts through a boolean expression           |
| `openobserve_slo`                 | Service level objectives, with error budgets and burn-rate alerting     |
| `openobserve_function`            | VRL and JavaScript transforms, compiled by the server on save           |
| `openobserve_pipeline_destination`| External endpoints a pipeline forwards records to                       |
| `openobserve_pipeline`            | Graphs that transform records in flight between streams                 |
| `openobserve_ingestion_token`     | Credentials for collectors and agents sending data in                   |
| `openobserve_synthetic` ‡         | HTTP, TCP, TLS, SSH, and browser checks run from probe locations        |

## Data Sources

| Name                               | Description                                          |
|------------------------------------|------------------------------------------------------|
| `openobserve_organization(s)`      | One organization, or all visible ones                |
| `openobserve_stream(s)`            | One stream with schema and stats, or a listing       |
| `openobserve_user(s)`              | One user with roles and groups, or a listing         |
| `openobserve_user_roles`           | Built-in role names this deployment accepts          |
| `openobserve_service_accounts`     | Service accounts in an organization                  |
| `openobserve_role(s)` †            | One custom role with permissions, or a listing       |
| `openobserve_group(s)` †           | One group with members, or a listing                 |
| `openobserve_resources` †          | Resource types that can appear in a permission       |
| `openobserve_folder(s)`            | One folder by ID or name, or a listing               |
| `openobserve_dashboard(s)`         | One dashboard with its JSON, or a listing            |
| `openobserve_alert_template(s)`    | One template (including prebuilt ones), or a listing |
| `openobserve_alert_destination(s)` | One destination, or a listing                        |
| `openobserve_alert(s)`             | One alert by ID or name, or a listing                |
| `openobserve_composite_alert`      | One composite with each child's current state        |
| `openobserve_composite_alert_references` | Composites that hold a given alert as a child  |
| `openobserve_slo(s)`               | One objective with its measurement, or a listing     |
| `openobserve_function(s)`          | One function with the pipelines using it, or a listing |
| `openobserve_pipelines`            | Pipelines, with the ids an import needs              |
| `openobserve_ingestion_tokens`     | Tokens in an organization, without their values      |
| `openobserve_synthetics` ‡         | Synthetic checks, with the ids an import needs       |
| `openobserve_synthetic_locations` ‡| Probe locations, browsers, and viewports available   |

† Requires OpenObserve Enterprise with OpenFGA enabled. Against an open-source
deployment these return a diagnostic saying so rather than an opaque HTTP 403.
Everything else, including SLOs, works on both editions.

‡ Requires the server to run with `ZO_SYNTHETICS_ENABLED=true`. The routes are
not registered when it is off, so the provider turns the resulting 404 into a
diagnostic that says the feature is disabled.

## Documentation

Full reference and guides are on the
[Terraform Registry](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs):

- [Getting started](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/getting-started)
- [Alerting](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/alerting): every query type, warning thresholds, simple vs multi-alerts, composite alerts
- [Service level objectives](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/slos): indicators, error budgets, burn-rate alerts
- [Dashboards](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/dashboards): panel JSON without the guesswork
- [Roles and groups](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/rbac)
- [Pipelines](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/pipelines): functions, destinations, and the order they have to be created in
- [Synthetic monitoring](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/synthetics): check types, the check budget, and browser journeys
- [Ingestion tokens](https://registry.terraform.io/providers/openobserve/openobserve/latest/docs/guides/ingestion-tokens): issuing and revoking collector credentials

## Import

Every resource supports `terraform import`:

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
terraform import openobserve_ingestion_token.example   default/otel-collector
terraform import openobserve_synthetic.example         default/3Ip9aYhgjr5Ozj5bzb58deBuE2s
```

A service account's API token is only ever returned when the account is created
or its token is rotated, so an imported account has an empty `token`. Change
`rotate_token` to issue a fresh one. An ingestion token behaves the same way, but
has no rotation: issue a new one and disable the old.

## Local Development

```bash
make build     # compile the provider
make install   # install into ~/.terraform.d/plugins for local testing
make test      # unit tests, no server required
make testacc   # acceptance tests against a live instance
make lint      # golangci-lint
make docs      # regenerate docs/ with tfplugindocs
```

### Testing against a live OpenObserve

The integration tests exercise every client call, including deletes, against a
real server. They skip unless `OPENOBSERVE_ENDPOINT` is set:

```bash
docker run -d --name o2 -p 5080:5080 \
  -e ZO_ROOT_USER_EMAIL=root@example.com \
  -e ZO_ROOT_USER_PASSWORD='Complexpass#123' \
  openobserve/openobserve:latest

OPENOBSERVE_ENDPOINT=http://localhost:5080 \
OPENOBSERVE_USERNAME=root@example.com \
OPENOBSERVE_PASSWORD='Complexpass#123' \
OPENOBSERVE_ORG_ID=default \
  go test ./internal/provider/ -run TestIntegration -v
```

Each test creates uniquely named objects and cleans up after itself.

## Publishing to the Terraform Registry

See [Publishing Providers](https://developer.hashicorp.com/terraform/registry/providers/publishing).
You need a GPG key on your Terraform Registry account and the `GPG_PRIVATE_KEY`
and `PASSPHRASE` secrets configured in GitHub Actions.

## License

Apache 2.0. See [LICENSE](LICENSE).
