data "openobserve_role" "log_reader" {
  name = "log_reader"
}

output "log_reader_grants" {
  value = data.openobserve_role.log_reader.permissions
}
