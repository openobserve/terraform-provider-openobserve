resource "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs"

  data_retention  = 30
  max_query_range = 168

  full_text_search_keys = ["message", "error"]
  index_fields          = ["level"]
  bloom_filter_fields   = ["trace_id"]

  # A field used as a partition key cannot also be a secondary index field.
  partition_keys = [
    { field = "service", type = "value" },
  ]
}

# A stream that keeps only a defined set of columns, with the rest folded into
# the catch-all field. Useful for high-cardinality sources.
resource "openobserve_stream" "metrics" {
  name        = "app_metrics"
  stream_type = "metrics"

  data_retention        = 90
  defined_schema_fields = ["service", "instance", "region"]
  store_original_data   = true

  partition_keys = [
    { field = "instance", type = "hash", hash_buckets = 16 },
  ]
}
