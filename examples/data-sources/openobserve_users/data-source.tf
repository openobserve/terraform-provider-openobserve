data "openobserve_users" "all" {}

output "admins" {
  value = [for u in data.openobserve_users.all.users : u.email if u.role == "admin"]
}
