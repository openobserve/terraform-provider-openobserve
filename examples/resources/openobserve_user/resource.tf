resource "openobserve_user" "alice" {
  email      = "alice@example.com"
  first_name = "Alice"
  last_name  = "Ng"
  role       = "admin"
  password   = var.alice_initial_password
}

# The same person in a second organization: the account already exists, so this
# grants them membership rather than creating a new user.
resource "openobserve_user" "alice_platform" {
  org_id = openobserve_organization.platform.identifier
  email  = "alice@example.com"
  role   = "viewer"
}

# A user whose access comes from a custom role instead of a built-in one.
resource "openobserve_user" "bob" {
  email        = "bob@example.com"
  first_name   = "Bob"
  role         = "viewer"
  password     = var.bob_initial_password
  custom_roles = [openobserve_role.log_reader.name]
}
