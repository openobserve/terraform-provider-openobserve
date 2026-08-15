data "openobserve_alert_templates" "all" {}

output "editable_templates" {
  value = [for t in data.openobserve_alert_templates.all.templates : t.name if !t.is_prebuilt]
}
