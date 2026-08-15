# Terraform Provider for OpenObserve

[![CI](https://github.com/openobserve/terraform-provider-openobserve/actions/workflows/ci.yml/badge.svg)](https://github.com/openobserve/terraform-provider-openobserve/actions/workflows/ci.yml)
[![Registry](https://img.shields.io/badge/Terraform_Registry-openobserve%2Fopenobserve-623CE4?logo=terraform)](https://registry.terraform.io/providers/openobserve/openobserve/latest)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Manage [OpenObserve](https://openobserve.ai) with Terraform: organizations, streams, dashboards, alerting, and IAM.

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
      version = "~> 1.0"
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
| `openobserve_alert`               | Scheduled and real-time alerts (SQL, PromQL, and aggregation)            |

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

† Requires OpenObserve Enterprise with OpenFGA enabled. Against an open-source
deployment these return a diagnostic saying so rather than an opaque HTTP 403.

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
```

A service account's API token is only ever returned when the account is created
or its token is rotated, so an imported account has an empty `token`. Change
`rotate_token` to issue a fresh one.

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

Apache 2.0 — see [LICENSE](LICENSE).
