resource "openobserve_dashboard" "service_overview" {
  org_id      = "default"
  title       = "Service Overview"
  description = "Ingestion rate and error budget for all services."

  # Full dashboard JSON — panels, variables, and layout.
  # Export from the OpenObserve UI or compose manually.
  definition = jsonencode({
    panels    = []
    variables = {}
  })
}
