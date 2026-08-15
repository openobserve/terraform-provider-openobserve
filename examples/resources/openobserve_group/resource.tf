resource "openobserve_group" "sre" {
  name = "sre"

  users = [
    openobserve_user.alice.email,
    openobserve_user.bob.email,
  ]

  roles = [
    openobserve_role.log_reader.name,
    openobserve_role.payments_owner.name,
  ]
}
