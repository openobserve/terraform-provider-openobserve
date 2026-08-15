# Reuse a prebuilt template that ships with OpenObserve.
data "openobserve_alert_template" "default" {
  name = "Default"
}

resource "openobserve_alert_destination" "webhook" {
  name     = "ops-webhook"
  url      = var.webhook_url
  template = data.openobserve_alert_template.default.name
}
