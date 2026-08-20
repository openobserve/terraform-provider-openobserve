---
page_title: "Getting started with OpenObserve"
subcategory: "Guides"
description: |-
  Configure the provider, create a stream, and wire up your first alert.
---

# Getting started

This walks from an empty configuration to a stream that is being watched by an
alert. Every step is a resource you can apply on its own.

## Configure the provider

Credentials come from the provider block or from the environment. Prefer the
environment so they stay out of your configuration and out of version control:

```bash
export OPENOBSERVE_ENDPOINT="https://openobserve.example.com"
export OPENOBSERVE_USERNAME="admin@example.com"
export OPENOBSERVE_PASSWORD="…"
export OPENOBSERVE_ORG_ID="default"
```

```hcl
terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.2"
    }
  }
}

provider "openobserve" {}
```

## Create a stream

A stream is where data lands. Applying `openobserve_stream` to a name that does
not exist creates it. Applying it to a stream that already exists, because data
has been ingested into it, adopts that stream and manages its settings.

```hcl
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
```

~> A field used as a partition key cannot also be a secondary index field. The
server rejects that combination, so keep the two lists disjoint.

## Send a notification somewhere

An alert needs a destination, and a destination needs a template. The template
renders the message; the destination says where it goes.

```hcl
resource "openobserve_alert_template" "slack" {
  name = "slack"
  body = jsonencode({
    text = "{alert_name} fired on {stream_name} at {alert_start_time}\n{alert_url}"
  })
}

resource "openobserve_alert_destination" "slack" {
  name     = "slack-platform"
  url      = var.slack_webhook_url
  template = openobserve_alert_template.slack.name
}
```

## Watch the stream

```hcl
resource "openobserve_alert" "high_error_rate" {
  name         = "high-error-rate"
  stream_type  = "logs"
  stream_name  = openobserve_stream.app_logs.name
  destinations = [openobserve_alert_destination.slack.name]

  query_condition {
    type = "sql"
    sql  = "SELECT count(_timestamp) AS total FROM \"app_logs\" WHERE level = 'error'"
  }

  trigger_condition {
    period    = 15 # look back 15 minutes
    operator  = ">="
    threshold = 100
    frequency = 5 # evaluate every 5 minutes
    silence   = 60
  }
}
```

That is a complete pipeline: a stream, a rendered message, a delivery channel,
and a rule that ties them together.

## Adopting what already exists

Every resource supports `terraform import`, so an OpenObserve instance that was
configured by hand can be brought under Terraform without recreating anything:

```bash
terraform import openobserve_stream.app_logs default/logs/app_logs
terraform import openobserve_alert.high_error_rate default/2fXkZ8QlmNbYcV1pR3sT
```

The data sources are the fastest way to find the identifiers an import needs:

```hcl
data "openobserve_alerts" "all" {}

output "alert_ids" {
  value = { for a in data.openobserve_alerts.all.alerts : a.name => a.alert_id }
}
```

An import populates state from the server, with no configuration to compare
against, so the first plan afterwards may show a diff as Terraform reconciles
what you wrote against what the server stored. Applying it converges; it does not
recreate anything.

## Where to go next

- [Alerting](alerting): every query type, warning thresholds, and per-group alerts
- [Service level objectives](slos): error budgets and burn-rate alerts
- [Dashboards](dashboards): panel JSON without the guesswork
- [Roles and groups](rbac): permissions, groups, and service accounts
