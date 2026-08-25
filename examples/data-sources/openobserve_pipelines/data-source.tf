data "openobserve_pipelines" "all" {}

# A pipeline's identifier is server-assigned, so this is the fastest way to find
# the id a `terraform import` needs.
output "pipeline_ids" {
  value = { for p in data.openobserve_pipelines.all.pipelines : p.name => p.pipeline_id }
}

output "paused_pipelines" {
  value = [for p in data.openobserve_pipelines.all.pipelines : p.name if !p.enabled]
}
