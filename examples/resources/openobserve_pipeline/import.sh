# Pipelines are imported as {org_id}/{pipeline_id}. The identifier is
# server-assigned, so find it with the openobserve_pipelines data source:
#
#   output "pipeline_ids" {
#     value = { for p in data.openobserve_pipelines.all.pipelines : p.name => p.pipeline_id }
#   }
terraform import openobserve_pipeline.redact default/7497861055431835648
