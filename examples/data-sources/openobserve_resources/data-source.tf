# The resource types that can be named in an openobserve_role permission.
data "openobserve_resources" "available" {}

output "grantable_objects" {
  value = {
    for r in data.openobserve_resources.available.resources :
    r.key => r.display_name if r.visible
  }
}
