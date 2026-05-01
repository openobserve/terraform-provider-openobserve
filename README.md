# Terraform Provider for OpenObserve

[![CI](https://github.com/openobserve/terraform-provider-openobserve/actions/workflows/ci.yml/badge.svg)](https://github.com/openobserve/terraform-provider-openobserve/actions/workflows/ci.yml)
[![Registry](https://img.shields.io/badge/Terraform_Registry-openobserve%2Fopenobserve-623CE4?logo=terraform)](https://registry.terraform.io/providers/openobserve/openobserve/latest)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

Manage [OpenObserve](https://openobserve.ai) resources with Terraform — streams, dashboards, users, and organizations.

## Requirements

| Tool      | Version  |
|-----------|----------|
| Terraform | >= 1.0   |
| Go        | >= 1.22  |

## Quick Start

```hcl
terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 0.1"
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
  org_id      = "default"
  name        = "app-logs"
  stream_type = "logs"

  settings {
    data_retention        = 30
    full_text_search_keys = ["message"]
    index_fields          = ["level", "service"]
  }
}
```

## Authentication

Pass credentials via environment variables to keep them out of state files:

```bash
export OPENOBSERVE_ENDPOINT="https://openobserve.example.com"
export OPENOBSERVE_USERNAME="admin@example.com"
export OPENOBSERVE_PASSWORD="your-password"
export OPENOBSERVE_ORG_ID="default"
```

## Resources & Data Sources

| Type          | Name                             | Description                              |
|---------------|----------------------------------|------------------------------------------|
| Resource      | `openobserve_stream`             | Manage stream settings                   |
| Resource      | `openobserve_dashboard`          | Manage dashboards (JSON definition)      |
| Resource      | `openobserve_user`               | Manage organization users                |
| Data Source   | `openobserve_stream`             | Read stream settings                     |
| Data Source   | `openobserve_organization`       | Read organization metadata               |

## Import

```bash
# Stream
terraform import openobserve_stream.example default/logs/app-logs

# Dashboard
terraform import openobserve_dashboard.example default/abc123

# User
terraform import openobserve_user.example default/user@example.com
```

## Local Development

```bash
# Build and install locally
make install

# Run unit tests
make test

# Run acceptance tests (requires a live OpenObserve instance)
make testacc

# Lint
make lint

# Regenerate docs
make docs
```

## Publishing to the Terraform Registry

See [Publishing Providers](https://developer.hashicorp.com/terraform/registry/providers/publishing) in the Terraform documentation.
You will need a GPG key added to your Terraform Registry account and the `GPG_PRIVATE_KEY` / `PASSPHRASE` secrets configured in GitHub Actions.

## License

Apache 2.0 — see [LICENSE](LICENSE).
