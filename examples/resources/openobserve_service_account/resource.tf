resource "openobserve_service_account" "ci" {
  email      = "ci-pipeline@example.com"
  first_name = "CI"
  last_name  = "Pipeline"

  custom_roles = [openobserve_role.ingest_only.name]
}

# The token is only ever returned at creation and after a rotation, so it lives
# in Terraform state. Feed it to whatever consumes it.
output "ci_token" {
  value     = openobserve_service_account.ci.token
  sensitive = true
}

# Rotating: change this value and apply. The previous token stops working.
resource "openobserve_service_account" "rotated_quarterly" {
  email        = "exporter@example.com"
  first_name   = "Metrics Exporter"
  rotate_token = "2026-Q3"
}
