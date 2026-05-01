data "openobserve_stream" "default_logs" {
  org_id      = "default"
  name        = "default"
  stream_type = "logs"
}

output "default_logs_retention" {
  value = data.openobserve_stream.default_logs.settings.data_retention
}
