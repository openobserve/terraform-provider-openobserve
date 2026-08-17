---
page_title: "Roles, groups, and service accounts"
subcategory: "Guides"
description: |-
  Permissions, group-based access, and non-human identities.
---

# Roles, groups, and service accounts

~> Custom roles, groups, and the permission resource list require OpenObserve
**Enterprise** with OpenFGA enabled. Against an open-source deployment they
return a diagnostic saying so. Users and service accounts work on both editions.

## Permissions

A permission binds a grant level to an object:

```hcl
resource "openobserve_role" "log_reader" {
  name = "log_reader"

  permissions = [
    { object = "stream", permission = "AllowList" },
    { object = "stream", permission = "AllowGet" },
    { object = "dashboard", permission = "AllowList" },
  ]
}
```

An object is either a **whole resource type** (`stream`) or a **single entity**
of that type (`stream:payments`).

OpenObserve stores a type-wide grant against an organization-scoped wildcard,
`stream:_all_myorg`. Writing the bare type is shorthand for exactly that, so the
organization identifier does not have to be repeated in every permission. Both
spellings are accepted, and whichever you write is what stays in state.

Grant levels are `AllowAll`, `AllowGet`, `AllowList`, `AllowPost`, `AllowPut`,
and `AllowDelete`. Which resource types exist depends on the deployment, so read
them rather than guessing:

```hcl
data "openobserve_resources" "available" {}

output "grantable" {
  value = { for r in data.openobserve_resources.available.resources : r.key => r.display_name if r.visible }
}
```

## Groups

Groups bundle users and grant them a set of roles, so membership changes do not
mean touching each role:

```hcl
resource "openobserve_group" "sre" {
  name  = "sre"
  users = [openobserve_user.alice.email, openobserve_user.bob.email]
  roles = [openobserve_role.log_reader.name]
}
```

## Assign a role from one side only

A role can be attached to a user in two places: `openobserve_role.users`, or
that user's `openobserve_user.custom_roles`. Both write the same underlying
grant.

!> Use one or the other. Doing both means two resources own the same fact, and
each will try to remove what the other adds, so every plan will show changes.

Members who hold a role through a group do not appear in `openobserve_role.users`,
because the group holds the grant rather than the user.

## Naming

Role and group names may contain only letters, digits, and underscores.
OpenObserve silently rewrites anything else to an underscore, which would leave
Terraform tracking a name the server does not have, so the provider rejects such
names during `plan` instead.

## Service accounts

A service account is a non-human identity that authenticates with an API token
rather than a password:

```hcl
resource "openobserve_service_account" "ci" {
  email        = "ci-pipeline@example.com"
  first_name   = "CI"
  custom_roles = [openobserve_role.ingest_only.name]
}

output "ci_token" {
  value     = openobserve_service_account.ci.token
  sensitive = true
}
```

The token is returned **only** when the account is created or its token is
rotated, so it lives in Terraform state. Treat your state file as a secret.

Rotate by changing the trigger value. The previous token stops working
immediately:

```hcl
resource "openobserve_service_account" "ci" {
  # …
  rotate_token = "2026-Q3"
}
```

An imported service account has an empty `token`, because OpenObserve never
returns an existing one. Change `rotate_token` to issue a fresh one.

## Users across organizations

To give one person access to several organizations, declare one
`openobserve_user` per organization with the same `email`. The first apply
creates the account; the rest grant membership. An account that already belongs
to the target organization is adopted rather than treated as an error.
