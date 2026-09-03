# A token authenticates data going *into* an organization. Collectors, agents
# and SDKs carry one. This is a different thing from a service account, which
# authenticates calls to the management API.
resource "openobserve_ingestion_token" "otel_collector" {
  name        = "otel-collector"
  description = "cluster-wide OTel collector"
}

# Issue one per sender rather than sharing a single token. That is what makes it
# possible to cut off one collector later without redeploying every other one.
resource "openobserve_ingestion_token" "mobile_sdk" {
  name        = "mobile-sdk"
  description = "iOS and Android RUM"
}

# Retiring a sender: disable rather than remove. OpenObserve has no delete
# endpoint for tokens, so this is the actual revoke, and the record stays for
# audit.
resource "openobserve_ingestion_token" "legacy_agent" {
  name        = "legacy-agent"
  description = "decommissioned 2026-Q3"
  enabled     = false
}

# The value is only returned when the token is created, so it lives in
# Terraform state. Keep state in an encrypted, access-controlled backend.
output "collector_token" {
  value     = openobserve_ingestion_token.otel_collector.token
  sensitive = true
}
