# Ingestion tokens are imported as {org_id}/{name}.
terraform import openobserve_ingestion_token.otel_collector default/otel-collector

# An imported token keeps whatever value state already held. The listing does
# report the secret, so an import of a token created elsewhere picks it up.
