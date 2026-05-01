resource "openobserve_stream" "app_logs" {
  org_id      = "default"
  name        = "app-logs"
  stream_type = "logs"

  settings {
    data_retention        = 30
    full_text_search_keys = ["message", "body"]
    index_fields          = ["level", "service"]
    bloom_filter_fields   = ["trace_id"]

    partition_keys {
      field = "service"
      types = ["value"]
    }
  }
}
