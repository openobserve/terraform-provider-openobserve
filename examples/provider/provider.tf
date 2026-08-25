terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.3"
    }
  }
}

provider "openobserve" {
  endpoint = "https://openobserve.example.com"
  username = var.openobserve_username
  password = var.openobserve_password

  # Used by every resource that does not set org_id itself.
  org_id = "default"
}

# Credentials can also come from the environment, which keeps them out of the
# configuration entirely:
#
#   export OPENOBSERVE_ENDPOINT=https://openobserve.example.com
#   export OPENOBSERVE_USERNAME=admin@example.com
#   export OPENOBSERVE_PASSWORD=...
#   export OPENOBSERVE_ORG_ID=default
