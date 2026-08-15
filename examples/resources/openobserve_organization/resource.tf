resource "openobserve_organization" "platform" {
  identifier = "platform"
  name       = "Platform Engineering"
}

# Resources in another organization reference it by identifier.
resource "openobserve_stream" "platform_logs" {
  org_id      = openobserve_organization.platform.identifier
  name        = "app_logs"
  stream_type = "logs"
}
