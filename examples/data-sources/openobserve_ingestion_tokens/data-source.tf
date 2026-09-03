data "openobserve_ingestion_tokens" "all" {}

# Auditing: which senders can still write into this organization.
output "active_tokens" {
  value = [for t in data.openobserve_ingestion_tokens.all.tokens : t.name if t.enabled]
}

output "revoked_tokens" {
  value = [for t in data.openobserve_ingestion_tokens.all.tokens : t.name if !t.enabled]
}

# Tokens nobody declared in Terraform, which is usually what you are looking for.
output "unmanaged_tokens" {
  value = [
    for t in data.openobserve_ingestion_tokens.all.tokens :
    t.name if !contains(["otel-collector", "mobile-sdk"], t.name)
  ]
}

# The values are deliberately not exposed here. A listing is the wrong place to
# hand out live credentials.
