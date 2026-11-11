# The RUM intake accepts only a user's RUM token, and OpenObserve returns it
# only to that user. To get an organization's own token, sign in as a
# dedicated member of it through a provider alias.
resource "openobserve_user" "rum" {
  org_id   = openobserve_organization.app.id
  email    = "rum@example.com"
  role     = "admin"
  password = random_password.rum.result
}

provider "openobserve" {
  alias    = "rum"
  endpoint = "https://openobserve.example.com"
  username = "rum@example.com"
  password = random_password.rum.result
}

data "openobserve_rum_token" "app" {
  provider   = openobserve.rum
  org_id     = openobserve_organization.app.id
  depends_on = [openobserve_user.rum]
}

# Hand the token to the site's RUM SDK configuration.
output "rum_client_token" {
  value     = data.openobserve_rum_token.app.token
  sensitive = true
}
