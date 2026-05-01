terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 0.1"
    }
  }
}

# Provider configuration using explicit values.
# Prefer environment variables for credentials in production:
#   OPENOBSERVE_ENDPOINT, OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID
provider "openobserve" {
  endpoint = "http://localhost:5080"
  username = "root@example.com"
  password = "Complexpass#123"
  org_id   = "default"
}
