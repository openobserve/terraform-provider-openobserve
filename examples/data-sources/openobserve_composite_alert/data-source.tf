data "openobserve_composite_alert" "bad_deploy" {
  name = "checkout-bad-deploy"
}

# Why is it firing, or why is it not? `children` carries the state the
# composite actually read, which is the fastest way to answer that.
output "children_currently_true" {
  value = [
    for child in data.openobserve_composite_alert.bad_deploy.children :
    child.name if child.truth
  ]
}

# A stale child is not contributing its last value; it is contributing whatever
# stale_child_policy says. That distinction is usually the answer to "why did
# this not fire".
output "stale_children" {
  value = [
    for child in data.openobserve_composite_alert.bad_deploy.children :
    child.name if child.stale
  ]
}

# Null until the composite has been evaluated once, which is not the same as
# having evaluated to false.
output "last_result" {
  value = try(data.openobserve_composite_alert.bad_deploy.evaluation.result, null)
}
