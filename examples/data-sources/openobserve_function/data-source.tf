data "openobserve_function" "redact" {
  name = "redact_email"
}

# Which pipelines hold this function. This answers "why can I not delete it":
# the server refuses to remove a function that is still referenced.
output "used_by" {
  value = data.openobserve_function.redact.used_by
}

output "safe_to_delete" {
  value = data.openobserve_function.redact.used_by_count == 0
}
