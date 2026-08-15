data "openobserve_groups" "all" {}

output "group_names" {
  value = data.openobserve_groups.all.groups
}
