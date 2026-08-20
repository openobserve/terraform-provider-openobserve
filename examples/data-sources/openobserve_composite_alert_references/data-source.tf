# The server refuses to delete an alert while a composite still names it as a
# child. This answers "what is holding on to this alert", including composites
# that were created outside Terraform.
data "openobserve_composite_alert_references" "error_rate" {
  alert_id = openobserve_alert.error_rate.alert_id
}

output "blocking_composites" {
  value = [
    for ref in data.openobserve_composite_alert_references.error_rate.references :
    ref.name
  ]
}

# Permissions can hide referencing composites from the caller. When this is not
# zero, an empty references list does not mean the alert is unreferenced, and a
# delete may still be refused.
output "references_hidden_by_permissions" {
  value = data.openobserve_composite_alert_references.error_rate.hidden_reference_count
}
