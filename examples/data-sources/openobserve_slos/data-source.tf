data "openobserve_slos" "all" {}

# Objectives that have already overspent their error budget.
output "over_budget" {
  value = [
    for s in data.openobserve_slos.all.slos : s.name
    if s.status != null && s.status.error_budget_remaining != null && s.status.error_budget_remaining < 0
  ]
}

# Objectives frozen because too little of the window was measured. These are
# neither healthy nor breached, and are easy to miss without looking.
output "frozen" {
  value = [for s in data.openobserve_slos.all.slos : s.name if s.status != null && s.status.no_data]
}
