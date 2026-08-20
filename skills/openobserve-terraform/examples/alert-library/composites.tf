# Composite alerts are the thing the library's JSON export format cannot
# express, and the thing that turns a pile of good signals into a small number
# of pages.
#
# Each references alerts the library already installed, by looking them out of
# the for_each map. Nothing is duplicated: the queries stay where the library
# maintains them, and only the correlation lives here.
#
# These reference the k8s pack. Remove this file, or adjust the keys, when
# installing a different pack.

# Node memory pressure alone is noise. Node memory pressure while pods are being
# OOM killed on that cluster is a capacity incident.
resource "openobserve_composite_alert" "memory_capacity_incident" {
  count = alltrue([
    contains(keys(openobserve_alert.library), "k8s/node_memory_pressure"),
    contains(keys(openobserve_alert.library), "k8s/pod_oom_killed"),
  ]) ? 1 : 0

  name         = "k8s_memory_capacity_incident"
  folder_id    = openobserve_folder.library.folder_id
  destinations = [openobserve_alert_destination.library.name]
  description  = "Node memory pressure with pods being OOM killed"

  expression = "{${openobserve_alert.library["k8s/node_memory_pressure"].alert_id}} && {${openobserve_alert.library["k8s/pod_oom_killed"].alert_id}}"

  silence  = 30
  priority = 1
  tags     = ["k8s", "composite", "critical"]
}

# A node going NotReady while pods cannot be scheduled means capacity has
# actually been lost, rather than one node being restarted.
resource "openobserve_composite_alert" "capacity_lost" {
  count = alltrue([
    contains(keys(openobserve_alert.library), "k8s/node_not_ready"),
    contains(keys(openobserve_alert.library), "k8s/pod_pending_too_long"),
  ]) ? 1 : 0

  name         = "k8s_capacity_lost"
  folder_id    = openobserve_folder.library.folder_id
  destinations = [openobserve_alert_destination.library.name]
  description  = "A node is NotReady and pods are stuck pending"

  expression = "{${openobserve_alert.library["k8s/node_not_ready"].alert_id}} && {${openobserve_alert.library["k8s/pod_pending_too_long"].alert_id}}"

  # A node that stops reporting is exactly the case this alert exists for, so a
  # stale child must not quietly stop satisfying the expression.
  stale_child_policy = "treat_as_true"

  silence  = 15
  priority = 1
  tags     = ["k8s", "composite", "critical"]
}

# Crashlooping is only worth paging on when it is not already explained by an
# image pull problem, which has a different fix and its own alert.
resource "openobserve_composite_alert" "unexplained_crashloop" {
  count = alltrue([
    contains(keys(openobserve_alert.library), "k8s/pod_crashloop_backoff"),
    contains(keys(openobserve_alert.library), "k8s/pod_image_pull_backoff"),
  ]) ? 1 : 0

  name         = "k8s_unexplained_crashloop"
  folder_id    = openobserve_folder.library.folder_id
  destinations = [openobserve_alert_destination.library.name]
  description  = "Containers crashlooping for a reason other than image pull"

  expression = "{${openobserve_alert.library["k8s/pod_crashloop_backoff"].alert_id}} && !{${openobserve_alert.library["k8s/pod_image_pull_backoff"].alert_id}}"

  silence  = 30
  priority = 2
  tags     = ["k8s", "composite", "critical"]
}
