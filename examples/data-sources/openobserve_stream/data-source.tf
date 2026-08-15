data "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs"
}

output "app_log_fields" {
  value = [for f in data.openobserve_stream.app_logs.schema : f.name]
}

output "app_log_retention_days" {
  value = data.openobserve_stream.app_logs.data_retention
}
