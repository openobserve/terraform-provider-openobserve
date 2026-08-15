data "openobserve_service_accounts" "all" {}

output "service_account_emails" {
  value = [for a in data.openobserve_service_accounts.all.service_accounts : a.email]
}
