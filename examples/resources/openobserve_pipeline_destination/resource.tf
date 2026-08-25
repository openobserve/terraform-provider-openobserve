# Where a pipeline forwards records. This is the same underlying object as an
# alert destination; the absence of a template is what makes it a pipeline one.
# The server resolves the URL when the destination is saved, and rejects one it
# cannot reach with "Destination URL blocked by SSRF guard". The host has to
# exist by the time Terraform applies.
resource "openobserve_pipeline_destination" "warehouse" {
  name = "warehouse"
  url  = "https://example.com/ingest"

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
