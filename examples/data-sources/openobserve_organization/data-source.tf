data "openobserve_organization" "default" {
  identifier = "default"
}

output "org_name" {
  value = data.openobserve_organization.default.name
}
