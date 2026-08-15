resource "openobserve_alert_template" "slack" {
  name = "slack"
  type = "http"

  body = jsonencode({
    text = "{alert_name} fired on {stream_name} at {alert_start_time}\n{alert_url}"
  })
}

resource "openobserve_alert_template" "email" {
  name  = "oncall_email"
  type  = "email"
  title = "[{org_name}] {alert_name}"

  body = <<-EOT
    Alert {alert_name} fired on stream {stream_name}.

    Window: {alert_start_time} to {alert_end_time}

    {rows}

    Open in OpenObserve: {alert_url}
  EOT
}
