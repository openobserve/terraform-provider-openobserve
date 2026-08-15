# The set of built-in roles differs between the open-source and Enterprise
# editions, so read it rather than hard-coding role names.
data "openobserve_user_roles" "available" {}

output "available_roles" {
  value = data.openobserve_user_roles.available.roles
}
