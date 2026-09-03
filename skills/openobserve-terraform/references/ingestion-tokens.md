# Ingestion tokens

An ingestion token authenticates data going *into* an organization. Collectors,
agents and SDKs carry one.

## Not a service account

This is the most common confusion in this area, so check it first when a user
says "token":

| | Authenticates | Carried by |
|---|---|---|
| `openobserve_ingestion_token` | Data going in | OTel collectors, agents, RUM SDKs |
| `openobserve_service_account` | Calls to the management API | CI, Terraform, scripts |

A service account cannot ingest; an ingestion token cannot manage anything.
If the user is configuring an exporter, they want an ingestion token. If they
are configuring Terraform itself, they want a service account.

## Shape

```hcl
resource "openobserve_ingestion_token" "otel_collector" {
  name        = "otel-collector"
  description = "cluster-wide OTel collector"
}
```

| Attribute | |
|---|---|
| `name` | Required, unique in the org, `RequiresReplace` |
| `description` | Optional |
| `enabled` | Optional, defaults true. False stops the token being accepted |
| `org_id` | Optional, falls back to the provider's, `RequiresReplace` |
| `token` | Computed, sensitive. The value to configure a collector with |
| `is_default` | Computed. The server decides, not you |
| `created_by`, `created_at` | Computed. `created_at` is microseconds |

Issue one per sender. A shared token cannot be withdrawn from one sender
without redeploying every other one. Name it after the sender so the audit
trail stays readable.

## There is no delete, and this has consequences

OpenObserve exposes no delete endpoint for ingestion tokens. This is the one
resource where the provider cannot make the server match the configuration.

Removing the resource **disables** the token and warns:

```
Warning: Ingestion token disabled rather than deleted

OpenObserve has no API for deleting an ingestion token, so "otel-collector"
has been disabled and no longer authenticates ingestion. The record remains
visible in the organization, and Terraform no longer tracks it. Re-creating a
token with the same name will fail while that record exists.
```

Two consequences worth stating to a user before they hit them:

**The name is burned.** Because the record survives and names are unique,
`terraform destroy` followed by `terraform apply` fails with
`Token with name 'x' already exists in this org`. Cycling a credential means
picking a new name, not reusing the old one. This also means the standard
verification loop (apply, re-apply, import, destroy, re-apply) cannot be run
repeatedly against the same names.

**Disabling is the real revocation.** A disabled token stops being accepted
immediately. Prefer expressing retirement in the configuration rather than by
deletion, so the intent stays in the code:

```hcl
resource "openobserve_ingestion_token" "legacy_agent" {
  name        = "legacy-agent"
  description = "decommissioned 2026-Q3"
  enabled     = false
}
```

## The value lives in state

The token is only returned at creation. It cannot be fetched again.

- Anyone who can read state can ingest into the organization. Use an encrypted,
  access-controlled backend.
- Mark outputs `sensitive = true`.
- A lost value cannot be recovered: issue a new token, disable the old one.
- An imported token has an empty `token`, for the same reason an imported
  service account does.

The `openobserve_ingestion_tokens` data source deliberately **does not expose
token values**. It reports `name`, `description`, `enabled`, `is_default`,
`created_by` and `created_at`, which is enough to audit what is issued without
handing out live credentials to anything that can read the org.

```hcl
data "openobserve_ingestion_tokens" "all" {}

output "token_names" {
  value = [for t in data.openobserve_ingestion_tokens.all.tokens : t.name]
}
```

## Import

```console
$ terraform import openobserve_ingestion_token.example default/otel-collector
```

By name, not id, because the name is what is unique and what the API addresses.
