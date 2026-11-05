# Where a pipeline forwards records. This is the same underlying object as an
# alert destination; the provider stores it as a pipeline one by sending
# module=pipeline on create and update.
# The server resolves the URL when the destination is saved, and rejects one it
# cannot reach with "Destination URL blocked by SSRF guard". The host has to
# exist by the time Terraform applies.
resource "openobserve_pipeline_destination" "warehouse" {
  name = "warehouse"
  url  = "https://example.com/ingest"

  # One JSON object per line. Leave unset to send one JSON array per request.
  output_format = "ndjson"

  headers = {
    Authorization = "Bearer ${var.warehouse_token}"
  }
}

# For a system OpenObserve knows how to format for, name it. Leave it unset for
# a plain webhook.
resource "openobserve_pipeline_destination" "splunk" {
  name             = "splunk_hec"
  url              = "https://example.com/services/collector"
  destination_type = "splunk"
  skip_tls_verify  = false

  metadata = {
    team = "platform"
  }
}

# Elasticsearch bulk format needs the index to write into.
resource "openobserve_pipeline_destination" "elasticsearch" {
  name                = "es_bulk"
  url                 = "https://example.com/_bulk"
  destination_type    = "elasticsearch"
  output_format       = "esbulk"
  output_format_index = "app-logs"
}
