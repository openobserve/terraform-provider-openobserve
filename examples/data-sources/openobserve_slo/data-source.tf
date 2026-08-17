data "openobserve_slo" "checkout" {
  name = "checkout_availability"
}

# `status` is null until the first evaluation pass has run. "Not yet measured"
# and "measured as zero" are different answers, so the provider does not
# conflate them.
output "budget_remaining_pct" {
  value = data.openobserve_slo.checkout.status.error_budget_remaining
}

output "burning_faster_than_budget" {
  value = try(data.openobserve_slo.checkout.status.burn_rate > 1, false)
}
