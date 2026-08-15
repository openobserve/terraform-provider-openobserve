# Read-only access to every stream and dashboard.
#
# A bare resource type is shorthand for the organization-wide wildcard that
# OpenObserve stores, so `stream` here means `stream:_all_<org_id>`.
resource "openobserve_role" "log_reader" {
  name = "log_reader"

  permissions = [
    { object = "stream", permission = "AllowList" },
    { object = "stream", permission = "AllowGet" },
    { object = "dashboard", permission = "AllowList" },
    { object = "dashboard", permission = "AllowGet" },
  ]
}

# Full control over one specific stream, granted directly to two people.
# Naming an entity requires the `{type}:{name}` form.
resource "openobserve_role" "payments_owner" {
  name = "payments_owner"

  permissions = [
    { object = "stream:payments", permission = "AllowAll" },
  ]

  users = [
    openobserve_user.alice.email,
    "carol@example.com",
  ]
}

# Which resource types can appear in `object` depends on the deployment.
data "openobserve_resources" "available" {}

output "permission_objects" {
  value = [for r in data.openobserve_resources.available.resources : r.key if r.visible]
}
