data "openobserve_organizations" "all" {}

output "organization_ids" {
  value = [for o in data.openobserve_organizations.all.organizations : o.identifier]
}
