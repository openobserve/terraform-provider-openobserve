variable "synthetic_username" {
  type    = string
  default = "probe@example.com"
}

resource "openobserve_alert_template" "slack" {
  name = "synthetic_tmpl"
  type = "http"
  body = jsonencode({ text = "{alert_name}" })
}

resource "openobserve_alert_destination" "slack" {
  name     = "synthetic_dest"
  type     = "http"
  url      = "https://example.com/hook"
  method   = "post"
  template = openobserve_alert_template.slack.name
}

# Locations are registered out of band, by running a probe agent or by
# OpenObserve for its public regions, so they are read rather than declared.
data "openobserve_synthetic_locations" "all" {}

resource "openobserve_folder" "synthetics" {
  folder_type = "synthetics"
  name        = "Synthetics"
}

# An HTTP check: request a URL on a schedule and page when it fails.
resource "openobserve_synthetic" "api_up" {
  name        = "api_up"
  description = "Public API reachable and healthy"
  folder_id   = openobserve_folder.synthetics.folder_id

  type   = "http"
  target = "https://api.example.com/health"

  frequency_type     = "minutes"
  frequency_interval = 5

  # Identifiers, not labels. A location the deployment does not have is
  # rejected at apply time.
  locations    = data.openobserve_synthetic_locations.all.names
  destinations = [openobserve_alert_destination.slack.name]

  # Ride out a single flaky probe rather than paging on it.
  retries                = 1
  wait_before_retry_secs = 10
  alert_if_fails         = 2
  cooldown_mins          = 15

  tags = ["prod", "api"]
}

# A TLS check catches the certificate expiry nobody diarised.
resource "openobserve_synthetic" "cert_expiry" {
  name      = "cert_expiry"
  folder_id = openobserve_folder.synthetics.folder_id

  type   = "tls"
  target = "api.example.com:443"

  frequency_type     = "hours"
  frequency_interval = 12

  locations    = data.openobserve_synthetic_locations.all.names
  destinations = [openobserve_alert_destination.slack.name]
}

# Type-specific settings live in `config`, because the shape differs per check
# kind. Build one in the UI and read it back with the openobserve_synthetics
# data source rather than guessing.
resource "openobserve_synthetic" "login_journey" {
  name      = "login_journey"
  folder_id = openobserve_folder.synthetics.folder_id

  type   = "browser"
  target = "https://app.example.com/login"

  frequency_type     = "minutes"
  frequency_interval = 15

  locations    = data.openobserve_synthetic_locations.all.names
  destinations = [openobserve_alert_destination.slack.name]

  # A journey is normally recorded in the UI and exported, rather than written
  # by hand. The shape is worth knowing anyway, because that export is what ends
  # up in version control:
  #
  #   id       a stable identifier for the step, unique within the journey. The
  #            recorder assigns it and self-healing keys off it, so keep it
  #            stable across edits rather than renumbering.
  #   action   navigate, click, hover, fill, press, select, check, uncheck,
  #            upload or assert. The first step must be navigate.
  #   locator  an ordered list of candidate selectors, best first. The probe
  #            tries each in turn, which is what lets a journey survive a markup
  #            change. kind is test_attribute, role, text, css or xpath.
  #
  # fill and select additionally require a value.
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
      # An assert step is what turns a journey into a test rather than a
      # click-through: without one, a login that silently lands on an error
      # page still passes. Assertion kinds are element_visible,
      # element_not_visible, element_text, url_matches, page_title and
      # element_attribute. The visibility kinds take no `expected`; the page
      # level ones, url_matches and page_title, take no locator.
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

    # Which browser and viewport combinations to run. Every combination is a
    # separate run against the budget below, so adding one here can push a
    # check over it. data.openobserve_synthetic_locations lists what is
    # available on the server.
    browser_devices = [
      { browser = "chromium", device = "desktop" },
    ]
  })

  # Browser-only, and rejected during plan on any other check type.
  collect_rum_data = true
  session_replay   = true

  # Retries multiply the run cost, and the worst case has to fit inside the
  # check budget: a fixed ceiling on one run, 840 seconds unless the server
  # changes ZO_SYNTHETICS_MAX_CHECK_BUDGET_SECS. It is not derived from the
  # schedule, so a longer frequency does not buy more room.
  #
  #   combos x (attempts x journey_budget_ms + retries x wait_before_retry_secs)
  #
  # One combo at the default 5 minute journey budget allows 2 attempts, so
  # retries is 1. Asking for 2 gives 3 attempts and is refused:
  #
  #   validation: config: this check needs up to 15m per run, which is over the
  #   14m check budget. To fix it, lower retries below 2, or shorten the run
  #   with config.journey_budget_ms
  #
  # Browser checks are capped at 2 retries regardless; the budget bites first.
  retries = 1

  # Parameterise rather than hardcoding, so the same check works per environment.
  variable {
    name  = "username"
    value = var.synthetic_username
  }

  # Prefer a stored secret to a literal: the credential never enters state.
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
