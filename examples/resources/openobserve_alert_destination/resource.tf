resource "openobserve_alert_destination" "slack" {
  name     = "slack-platform"
  type     = "http"
  url      = var.slack_webhook_url
  method   = "post"
  template = openobserve_alert_template.slack.name
}

resource "openobserve_alert_destination" "pagerduty" {
  name     = "pagerduty"
  type     = "http"
  url      = "https://events.pagerduty.com/v2/enqueue"
  template = openobserve_alert_template.slack.name

  headers = {
    "Content-Type" = "application/json"
  }
}

# Every address must belong to a user in the organization.
resource "openobserve_alert_destination" "oncall_email" {
  name     = "oncall-email"
  type     = "email"
  template = openobserve_alert_template.email.name
  emails   = [openobserve_user.alice.email]
}

resource "openobserve_alert_destination" "sns" {
  name          = "sns-alerts"
  type          = "sns"
  template      = openobserve_alert_template.slack.name
  sns_topic_arn = "arn:aws:sns:us-east-1:123456789012:openobserve-alerts"
  aws_region    = "us-east-1"
}
