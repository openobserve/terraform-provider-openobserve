---
page_title: "Ingestion tokens"
subcategory: "Guides"
description: |-
  Issuing credentials for collectors and agents sending data into OpenObserve.
---

# Ingestion tokens

An ingestion token authenticates data going *into* an organization. Collectors,
agents and SDKs carry one.

## Not the same thing as a service account

These are two different credentials solving two different problems, and mixing
them up is the most common mistake here:

| | Authenticates | Used by |
|---|---|---|
| `openobserve_ingestion_token` | Data going in | OTel collectors, agents, RUM SDKs |
| `openobserve_service_account` | Calls to the management API | CI, Terraform, scripts |

A service account cannot ingest, and an ingestion token cannot manage anything.

## Issue one per sender

```hcl
resource "openobserve_ingestion_token" "otel_collector" {
  name        = "otel-collector"
  description = "cluster-wide OTel collector"
}

resource "openobserve_ingestion_token" "mobile_sdk" {
  name        = "mobile-sdk"
  description = "iOS and Android RUM"
}
```

The reason is revocation. A shared token cannot be withdrawn from one sender
without redeploying all of them. Name the token after the sender so the audit
trail is readable a year later.

## There is no delete

OpenObserve exposes no delete endpoint for ingestion tokens. This is the one
place where the provider cannot make the server match your configuration, so it
is worth understanding what actually happens.

Removing an `openobserve_ingestion_token` from your configuration **disables**
the token rather than removing it, and the provider warns you:

```
Warning: Ingestion token disabled rather than deleted

OpenObserve has no API for deleting an ingestion token, so "otel-collector"
has been disabled and no longer authenticates ingestion. The record remains
visible in the organization, and Terraform no longer tracks it. Re-creating a
token with the same name will fail while that record exists.
```

Disabling is the real revocation: a disabled token stops being accepted
immediately. The record staying behind is deliberate on the server's part, so
that a token that was once used to write data can still be traced.

The sting is in the last sentence. Because the record survives and names are
unique, **you cannot remove a token and then re-add one with the same name.**
If you are cycling a credential, give the replacement a new name rather than
reusing the old one.

To retire a sender explicitly rather than by removal, keep the resource and
turn it off. This reads better in a diff and leaves the intent in the code:

```hcl
resource "openobserve_ingestion_token" "legacy_agent" {
  name        = "legacy-agent"
  description = "decommissioned 2026-Q3"
  enabled     = false
}
```

## The value is in state

The token value is only returned when the token is created, so the provider
stores it. There is no way to fetch it again afterwards, which is also why the
`openobserve_ingestion_tokens` data source deliberately does not expose token
values: a data source that handed out live credentials to anyone who could read
the organization would be a worse default than a slightly less convenient one.

Consequences worth planning for:

- **Keep state in an encrypted, access-controlled backend.** Anyone who can
  read state can ingest into your organization.
- Mark outputs sensitive:

  ```hcl
  output "collector_token" {
    value     = openobserve_ingestion_token.otel_collector.token
    sensitive = true
  }
  ```

- If you lose the value, issue a new token and disable the old one. It cannot
  be recovered.

## Finding what already exists

```hcl
data "openobserve_ingestion_tokens" "all" {}

output "token_names" {
  value = [for t in data.openobserve_ingestion_tokens.all.tokens : t.name]
}
```

This reports names, descriptions, whether each is enabled and who created it,
which is enough to audit what is issued without exposing anything usable.
