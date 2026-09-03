# Synthetic monitoring: probing from outside, and notifying when it fails.
#
# Requires the server to run with ZO_SYNTHETICS_ENABLED=true, and at least one
# registered probe location. Without a location every check saves and then never
# runs.
#
# Credentials come from the environment: OPENOBSERVE_ENDPOINT,
# OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID.

terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.4"
    }
  }
}

provider "openobserve" {}

variable "synthetic_username" {
  description = "Login used by the browser journey."
  type        = string
  default     = "probe@example.com"
}

# --- where checks live and where failures go ---------------------------------

# Synthetics have their OWN folder type. Pointing a check at an `alerts` folder
# fails with an opaque `FOREIGN KEY constraint failed (787)` rather than
# anything naming the folder. Note this differs from SLOs, which do live in
# alert folders.
resource "openobserve_folder" "synthetics" {
  name        = "synthetics"
  folder_type = "synthetics"
  description = "Synthetic checks"
}

resource "openobserve_alert_template" "slack" {
  name = "synthetic_slack"
  body = jsonencode({
    text = "{alert_name} failed: {stream_name}"
  })
}

# A destination needs a template. One created without a template becomes a
# pipeline destination and cannot be used by a check.
resource "openobserve_alert_destination" "slack" {
  name     = "synthetic_slack"
  url      = "https://hooks.slack.com/services/T000/B000/XXXX"
  method   = "post"
  template = openobserve_alert_template.slack.name
}

# Locations are registered by the deployment, not by Terraform. Read them.
data "openobserve_synthetic_locations" "all" {}

# --- the simplest useful check ------------------------------------------------

resource "openobserve_synthetic" "api_up" {
  name      = "api_up"
  folder_id = openobserve_folder.synthetics.folder_id

  type   = "http"
  target = "https://api.example.com/healthz"

  frequency_type     = "minutes"
  frequency_interval = 1

  locations    = data.openobserve_synthetic_locations.all.names
  destinations = [openobserve_alert_destination.slack.name]

  # Type-specific settings are JSON, because the five check types share almost
  # nothing. The provider compares this as JSON, so server-side key reordering
  # and defaults do not read as drift.
  config = jsonencode({
    method        = "GET"
    expect_status = 200
    timeout_ms    = 10000
  })

  retries = 2
}

# --- certificate expiry -------------------------------------------------------

resource "openobserve_synthetic" "cert_expiry" {
  name      = "cert_expiry"
  folder_id = openobserve_folder.synthetics.folder_id

  type   = "tls"
  target = "api.example.com:443"

  # Certificates change on a scale of months. Checking hourly is plenty, and a
  # long interval buys no extra run budget, so there is no cost to it either.
  frequency_type     = "hours"
  frequency_interval = 1

  locations    = data.openobserve_synthetic_locations.all.names
  destinations = [openobserve_alert_destination.slack.name]

  config = jsonencode({
    warn_days_before_expiry = 30
  })
}

# --- a browser journey --------------------------------------------------------

resource "openobserve_synthetic" "login_journey" {
  name      = "login_journey"
  folder_id = openobserve_folder.synthetics.folder_id

  type   = "browser"
  target = "https://app.example.com/login"

  frequency_type     = "minutes"
  frequency_interval = 15

  locations    = data.openobserve_synthetic_locations.all.names
  destinations = [openobserve_alert_destination.slack.name]

  # Record journeys in the UI and export them. The shape matters for review:
  # every step needs a stable `id`, the first must be `navigate`, and `locator`
  # is an ordered candidate list tried best-first, which is what lets a journey
  # survive a markup change.
  config = jsonencode({
    steps = [
      {
        id     = "s1"
        action = "navigate"
        url    = "https://app.example.com/login"
      },
      {
        id     = "s2"
        action = "fill"
        name   = "Email"
        value  = "$${username}"
        locator = {
          candidates = [
            { kind = "test_attribute", value = "login-email" },
            { kind = "css", value = "#email" },
          ]
        }
      },
      {
        id     = "s3"
        action = "click"
        name   = "Sign in"
        locator = {
          candidates = [
            { kind = "role", value = "button[name='Sign in']" },
            { kind = "css", value = "button[type=submit]" },
          ]
        }
      },
      # Without this step the journey is not a test: it would pass even when the
      # login lands on an error page, as long as nothing threw.
      {
        id     = "s4"
        action = "assert"
        name   = "Dashboard loaded"
        locator = {
          candidates = [
            { kind = "css", value = "#dashboard" },
          ]
        }
        assertion = {
          kind = "element_visible"
        }
      },
    ]

    browser_devices = [
      { browser = "chromium", device = "desktop" },
    ]
  })

  # Browser-only. Rejected during plan on any other type.
  collect_rum_data = true
  session_replay   = true

  # The worst case must fit the check budget, a fixed 840s ceiling that does NOT
  # come from the schedule:
  #
  #   combos x (attempts x journey_budget_ms + retries x wait_before_retry_secs)
  #
  # One combo at the default 5m journey budget allows 2 attempts, so retries is
  # 1. Asking for 2 gives 3 attempts and is refused at save time.
  retries = 1

  # Parameterise rather than hardcode, so one journey covers every environment.
  variable {
    name  = "username"
    value = var.synthetic_username
  }

  # Prefer a stored secret to a literal: `basic` and `bearer` put the credential
  # in Terraform state, `secret` names one already held by OpenObserve.
  auth {
    type        = "secret"
    secret_name = "synthetic-login"
  }

  cookie {
    name   = "consent"
    value  = "accepted"
    domain = "app.example.com"
  }
}

# --- what you have ------------------------------------------------------------

data "openobserve_synthetics" "all" {}

# A list rather than a name-keyed map: check names are unique per folder, not
# per organization, so two folders may each hold an `api_up` and a map would
# fail to build with "Two different items produced the key".
output "check_ids" {
  description = "Each check with the id terraform import needs."
  value = [
    for s in data.openobserve_synthetics.all.synthetics :
    { name = s.name, id = s.synthetic_id, folder = s.folder_id }
  ]
}

# A location whose status is `pending` has no agent checked in, and any check
# assigned to it saves successfully and then never runs. Check this first when
# results do not appear.
output "location_status" {
  value = {
    for l in data.openobserve_synthetic_locations.all.locations :
    l.location_id => { status = l.status, enabled = l.enabled, agents = l.live_agents }
  }
}
