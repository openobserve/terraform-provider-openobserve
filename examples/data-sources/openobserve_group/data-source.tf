data "openobserve_group" "sre" {
  name = "sre"
}

output "sre_members" {
  value = data.openobserve_group.sre.users
}
