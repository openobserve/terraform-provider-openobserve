---
page_title: "Synthetic monitoring"
subcategory: "Guides"
description: |-
  Probing endpoints and user journeys from outside your infrastructure, and the check budget that governs them.
---

# Synthetic monitoring

Everything else in this provider watches data you already have. A synthetic
check produces its own: it probes a target from one or more locations on a
schedule and notifies alert destinations when the probe fails. It is how you
find out that a login page is broken at 3am when nobody is logged in to notice.

## Before anything else: the feature has to be on

Synthetics routes are only registered when the server runs with
`ZO_SYNTHETICS_ENABLED=true`. When it is off the paths are not merely
forbidden, they do not exist, so every request answers `404` and the failure
looks identical to a missing check.

The provider detects this and says so:

```
Synthetics look disabled on this server. The routes are only registered when
ZO_SYNTHETICS_ENABLED is true, so every synthetics path answers 404 when it is
off. Enable it on the server, or remove the openobserve_synthetic resources.
```

## Checks live in synthetics folders

```hcl
resource "openobserve_folder" "synthetics" {
  name        = "Synthetics"
  folder_type = "synthetics"
}
```

There is a dedicated folder type, and it has to be used. Pointing a check at an
`alerts` folder fails with an opaque `FOREIGN KEY constraint failed (787)` that
names nothing useful. This is worth stating because it cuts against the SLO
rule, where objectives live in alert folders for want of a type of their own.

## Locations are not yours to create

A check runs from a location, and locations are registered by the deployment,
not by Terraform. There is no `openobserve_synthetic_location` resource on
purpose. Read them instead:

```hcl
data "openobserve_synthetic_locations" "all" {}

resource "openobserve_synthetic" "api_up" {
  # ...
  locations = data.openobserve_synthetic_locations.all.names
}
```

Two fields decide whether a location will actually run your check:

| Field | Why it matters |
|---|---|
| `enabled` | A disabled location is skipped |
| `status` | A private location with no agent checked in reads `pending`, and a check assigned to it never runs |

A check assigned only to a pending location is saved successfully and then
silently never executes. If checks are not producing results, look here first.

## The five check types

| Type | Probes | `target` |
|---|---|---|
| `http` | An HTTP endpoint, with assertions on status, headers and body | URL |
| `tcp` | A port accepts a connection | `host:port` |
| `tls` | A certificate, typically its expiry | `host:port` |
| `ssh` | An SSH endpoint | `host:port` |
| `browser` | A scripted journey in a real browser | Starting URL |

Type-specific settings live in `config`, a JSON document, because the five
types share almost nothing. Encode it rather than writing a string:

```hcl
config = jsonencode({
  method       = "GET"
  expect_status = 200
})
```

The provider compares `config` as JSON, not as text, so the server reordering
keys or filling in its own defaults does not show up as drift.

## The check budget

This is the rule that surprises people, so it is worth stating plainly.

**A single run has a fixed time ceiling, and it does not come from the
schedule.** The ceiling is `ZO_SYNTHETICS_MAX_CHECK_BUDGET_SECS`, 840 seconds
by default. Setting a check to run once a day does not buy it more room than
one that runs every minute.

The worst case is computed, not measured:

```
combos x (attempts x per_attempt + retries x wait_before_retry_secs)

  attempts     retries + 1
  per_attempt  config.timeout_ms, or config.journey_budget_ms for browser
  combos       browser/device combinations, 1 for non-browser checks
```

Exceed it and the write is refused:

```
validation: config: this check needs up to 15m per run, which is over the 14m
check budget. To fix it, lower retries below 2, or shorten the run with
config.journey_budget_ms
Detail: 1 browser/device combo(s) x 3 attempt(s) x 5m each
```

The three levers are in the message, in the order worth trying: fewer retries,
a shorter journey budget, or fewer browser/device combinations. Note that
combos multiply everything, so adding a second device to a journey that already
fits can push it over on its own.

`retries` is separately capped at 2 for browser checks. In practice the budget
usually bites first.

## Browser journeys

A journey is a list of steps. Record it in the UI and export it rather than
writing it by hand, then commit the export. The shape is still worth knowing,
because the export is what you will be reading in review:

```hcl
config = jsonencode({
  steps = [
    { id = "s1", action = "navigate", url = "https://app.example.com/login" },
    {
      id     = "s2"
      action = "fill"
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
      action = "assert"
      locator = { candidates = [{ kind = "css", value = "#dashboard" }] }
      assertion = { kind = "element_visible" }
    },
  ]
  browser_devices = [{ browser = "chromium", device = "desktop" }]
})
```

Rules the server enforces at save time:

- The first step must be `navigate`.
- Every step needs an `id`, unique within the journey. Self-healing keys off
  it, so keep ids stable across edits instead of renumbering.
- Actions are `navigate`, `click`, `hover`, `fill`, `press`, `select`,
  `check`, `uncheck`, `upload` and `assert`.
- `fill` and `select` require a `value`.
- `assert` requires an `assertion`. Kinds are `element_visible`,
  `element_not_visible`, `element_text`, `url_matches`, `page_title` and
  `element_attribute`. The visibility kinds take no `expected`; `url_matches`
  and `page_title` describe the page and take no locator.

`locator.candidates` is an ordered list, best first, and the probe tries each
in turn. That is what lets a journey survive a markup change, so prefer a
stable `test_attribute` first and a brittle `css` or `xpath` only as a fallback.

**A journey without an assert step is not a test.** It clicks through and
passes as long as nothing throws, including when the login lands on an error
page.

## Credentials

Three ways to authenticate, and they are not equally good:

```hcl
auth {
  type        = "secret"
  secret_name = "synthetic-login"    # preferred: never enters state
}
```

`basic` and `bearer` put the credential in Terraform state. `secret` refers to
one already stored in OpenObserve, so it does not. Use `secret` unless you
cannot.

Parameterise the rest with `variable` blocks rather than hardcoding, so one
journey works across environments. Mark anything sensitive:

```hcl
variable {
  name   = "password"
  value  = var.synthetic_password
  secure = true
}
```

A `secure` variable comes back from the server with its value emptied, so the
provider keeps the configured value rather than overwriting it with the
redaction. The consequence is that drift in a secure value cannot be detected:
if someone edits it in the UI, Terraform will not notice.

## Import

Checks import by id, not by name, because the id is what the API addresses:

```console
$ terraform import openobserve_synthetic.api_up default/3Ip9aYhgjr5Ozj5bzb58deBuE2s
```

Find it with the data source:

```hcl
data "openobserve_synthetics" "all" {}

output "ids" {
  value = [
    for s in data.openobserve_synthetics.all.synthetics :
    { name = s.name, id = s.synthetic_id, folder = s.folder_id }
  ]
}
```

A list, not a map keyed by name: check names are unique per folder rather than
per organization, so two folders may each hold an `api_up`.

An imported check brings its cookies and variables with it, but any `secure`
variable arrives with an empty value, and `auth` credentials arrive as the
server stores them. Fill those in from your own source of truth before the
first apply.
