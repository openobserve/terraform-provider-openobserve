data "openobserve_user" "alice" {
  email = "alice@example.com"
}

output "alice_groups" {
  value = data.openobserve_user.alice.groups
}
