data "openobserve_synthetic_locations" "all" {}

# `names` is the list of identifiers, ready to pass to a check.
output "all_locations" {
  value = data.openobserve_synthetic_locations.all.names
}

# A private location reads "pending" until an agent checks in, and a check
# assigned to it will not run even though it applies cleanly. This matters as
# much as `enabled`.
output "usable_locations" {
  value = [
    for l in data.openobserve_synthetic_locations.all.locations :
    l.location_id if l.enabled && l.status == "online"
  ]
}

output "locations_awaiting_an_agent" {
  value = [
    for l in data.openobserve_synthetic_locations.all.locations :
    l.label if l.status == "pending"
  ]
}

# What a browser check can run in and against. A run costs devices times
# attempts, so this is also what decides how expensive a check is.
output "browsers" { value = data.openobserve_synthetic_locations.all.browsers }
output "devices" { value = [for d in data.openobserve_synthetic_locations.all.devices : d.id] }
