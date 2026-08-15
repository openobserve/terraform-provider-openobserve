data "openobserve_roles" "all" {}

output "custom_roles" {
  value = data.openobserve_roles.all.roles
}
