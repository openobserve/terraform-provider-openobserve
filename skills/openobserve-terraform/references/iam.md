# Users, service accounts, roles and groups

| Resource | Edition |
|---|---|
| `openobserve_user` | Both |
| `openobserve_service_account` | Both |
| `openobserve_role` | Enterprise with OpenFGA |
| `openobserve_group` | Enterprise with OpenFGA |

Enterprise-only endpoints answer `403 {"code":403,"message":"Not Supported"}` on
open source. The provider converts that into a diagnostic naming the feature and
the required `ZO_OPENFGA_ENABLED=true`, rather than surfacing a bare 403.

---

## 1. Users

```hcl
resource "openobserve_user" "alice" {
  email      = "alice@example.com"
  first_name = "Alice"
  last_name  = "Smith"
  role       = "admin"
  password   = var.initial_password

  custom_roles = [openobserve_role.log_reader.name] # Enterprise
}
```

### Read the built-in role names, do not hardcode them

They differ by edition:

```hcl
data "openobserve_user_roles" "available" {}

output "roles" {
  value = data.openobserve_user_roles.available.roles
}
```

That endpoint returns `[{"label": "Admin", "value": "admin"}]`, objects rather
than bare strings. The provider decodes both shapes.

### Multi-organization membership

Declare one `openobserve_user` per organization with the same `email`. The first
apply creates the account; the rest grant membership.

```hcl
resource "openobserve_user" "alice_default" {
  email      = "alice@example.com"
  first_name = "Alice"
  role       = "admin"
  password   = var.initial_password
}

resource "openobserve_user" "alice_team" {
  org_id     = openobserve_organization.team.identifier
  email      = "alice@example.com"
  first_name = "Alice"
  role       = "viewer"
}
```

A user already belonging to the target organization returns `409 "User is
already part of the organization"` and is **adopted** rather than treated as an
error.

### Passwords

`password` is write-only from the server's perspective and is stored in
Terraform state. It is only used at creation; changing it updates the user's
password.

### Import

```bash
terraform import openobserve_user.alice default/alice@example.com
```

---

## 2. Service accounts

```hcl
resource "openobserve_service_account" "ci" {
  email        = "ci-pipeline@example.com"
  first_name   = "CI"
  rotate_token = "2026-Q3" # change this string to rotate
}

output "ci_token" {
  value     = openobserve_service_account.ci.token
  sensitive = true
}
```

> **The API token is returned only on creation and rotation**, so it lives in
> Terraform state. The list endpoint returns it redacted.

An imported service account has an empty `token`. Change `rotate_token` to issue
a fresh one.

`rotate_token` is an arbitrary string; the provider rotates whenever it changes.
Using a date or a quarter makes the intent legible in a diff.

### Import

```bash
terraform import openobserve_service_account.ci default/ci-pipeline@example.com
```

---

## 3. Roles and permissions (Enterprise)

```hcl
resource "openobserve_role" "log_reader" {
  name = "log_reader"

  permissions = [
    { object = "stream", permission = "AllowList" },
    { object = "stream:payments", permission = "AllowAll" },
    { object = "dashboard", permission = "AllowGet" },
  ]

  users = ["alice@example.com"]
}
```

### Object forms

An object is either a whole resource type or a single entity.

A **type-wide** grant is stored against an organization-scoped wildcard,
`stream:_all_myorg`. The bare type (`stream`) is shorthand that the provider
expands, and whichever spelling you write is what stays in state.

That is the **spelling preservation** drift rule: `stream` and
`stream:_all_myorg` name the same grant, the server only reports the expanded
form, and the provider keeps your spelling when the two describe the same set.

A **single-entity** grant names the entity: `stream:payments`.

### Grant levels

`AllowAll`, `AllowGet`, `AllowList`, `AllowPost`, `AllowPut`, `AllowDelete`.

The wire format is PascalCase; the OpenFGA relation stored underneath is
`ALLOW_ALL`. Write the PascalCase form.

### Valid object types

```hcl
data "openobserve_resources" "types" {}

output "object_types" {
  value = data.openobserve_resources.types.resources
}
```

### Names are silently rewritten by the server

Every character outside `[a-zA-Z0-9_]` becomes `_`, so `payments-owner` is
stored as `payments_owner` and Terraform would track a name that does not exist.

**The provider rejects such names during `plan`** rather than letting that
happen. Use underscores.

Note this differs from alert names, which the server rejects outright rather
than rewriting.

### Permissions are deltas

The role update endpoint takes `{add, remove}`, so the provider reads current
permissions, diffs, and sends the difference.

---

## 4. Groups (Enterprise)

```hcl
resource "openobserve_group" "sre" {
  name  = "sre"
  users = ["alice@example.com", "bob@example.com"]
  roles = [openobserve_role.log_reader.name]
}
```

Same name restriction as roles: `[a-zA-Z0-9_]` only, checked during `plan`.

### Two server bugs the provider works around

Both are worth knowing because they explain behaviour that looks wrong from the
API but is correct from Terraform.

1. **`create_group` ignores `roles` entirely.** Only the update endpoint
   attaches them.

2. **The server records the group-to-organization link only when the create
   request carries no users.** Creating a group with members writes only
   membership links, so the group never appears in the group list **or the UI**.

The provider always creates the group empty and populates it immediately after,
which also repairs a group created the other way.

If a group exists on the server but is invisible in the UI, it was created with
members through the raw API. Importing and applying with this provider fixes it.

---

## 5. Assign a role from one side only

`openobserve_role.users` and `openobserve_user.custom_roles` write the same
underlying grant. Using both means two resources own one fact, and each removes
what the other adds, so every plan shows changes.

Pick one direction and keep to it.

Members holding a role **via a group** do not appear in `openobserve_role.users`,
because the group holds the grant, not the user.

---

## 6. Reading IAM state

```hcl
data "openobserve_users" "all" {}
data "openobserve_user" "alice" { email = "alice@example.com" }

data "openobserve_roles" "all" {}
data "openobserve_role" "log_reader" { name = "log_reader" }

data "openobserve_groups" "all" {}
data "openobserve_group" "sre" { name = "sre" }

data "openobserve_service_accounts" "all" {}
```

`openobserve_user` exposes the user's `roles` and `groups`, which is the fastest
way to answer "why can this person see that stream".

## 7. Import

```bash
terraform import openobserve_role.log_reader default/log_reader
terraform import openobserve_group.sre       default/sre
```
