data "openobserve_streams" "logs" {
  stream_type = "logs"
}

# Streams that have not had their retention set explicitly.
output "streams_using_default_retention" {
  value = [
    for s in data.openobserve_streams.logs.streams : s.name
    if s.data_retention == 0
  ]
}
