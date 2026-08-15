data "openobserve_organization" "default" {
  identifier = "default"
}

output "org_owner" {
  value = data.openobserve_organization.default.user_email
}
