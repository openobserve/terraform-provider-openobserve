data "openobserve_synthetics" "all" {}

# A check's identifier is server-assigned, so this is how to find what an
# import needs.
output "synthetic_ids" {
  value = { for s in data.openobserve_synthetics.all.synthetics : s.name => s.synthetic_id }
}

output "paused_checks" {
  value = [for s in data.openobserve_synthetics.all.synthetics : s.name if !s.enabled]
}

# The fastest way to work out the `config` document a check type expects: build
# one in the UI, read it back here, and paste the result into jsonencode().
output "browser_check_configs" {
  value = {
    for s in data.openobserve_synthetics.all.synthetics :
    s.name => s.config if s.type == "browser"
  }
}
