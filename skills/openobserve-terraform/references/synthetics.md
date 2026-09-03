# Synthetic monitoring

Everything else in the provider watches data that already exists. A synthetic
check produces its own: it probes a target from probe locations on a schedule,
and notifies alert destinations when the probe fails.

Verified against a live server running with `ZO_SYNTHETICS_ENABLED=true`.

## The feature flag comes first

Synthetics routes are only registered when the server runs with
`ZO_SYNTHETICS_ENABLED=true`. When off, the paths do not exist, so every
request answers `404`, and a missing check is indistinguishable from a disabled
feature by status code alone.

The provider distinguishes them by whether the 404 body carries a JSON envelope
(a real route) or not (an unregistered one), and says which:

```
Synthetics look disabled on this server. The routes are only registered when
ZO_SYNTHETICS_ENABLED is true, so every synthetics path answers 404 when it is
off. Enable it on the server, or remove the openobserve_synthetic resources.
```

If a task involves synthetics, confirm the flag before debugging anything else.

## Synthetics have their own folder type

```hcl
resource "openobserve_folder" "synthetics" {
  name        = "Synthetics"
  folder_type = "synthetics"
}
```

`folder_type` is one of `dashboards`, `alerts`, `reports`, `synthetics`.
**Pointing a check at an `alerts` folder fails with an opaque
`FOREIGN KEY constraint failed (787)`** that names nothing useful.

Worth flagging because it cuts against the SLO rule: SLOs *do* live in alert
folders, because there is no SLO folder type. Synthetics have one, so they use
it.

## Locations are read, never created

There is no `openobserve_synthetic_location` resource, deliberately: locations
are registered by the deployment (agents check in), not by configuration.

```hcl
data "openobserve_synthetic_locations" "all" {}
```

| Attribute | What it gives you |
|---|---|
| `names` | Location ids, ready to pass to `locations` |
| `locations` | Full records: `location_id`, `label`, `region`, `provider`, `kind`, `pool`, `enabled`, `types`, `live_agents`, `agent_names`, `agents_total`, `status` |
| `browsers` | Browser names a browser check may use |
| `devices` | Viewports: `id`, `label`, `width`, `height` |

A check references a location by **`id`, not `label`**.

Two fields decide whether a check will actually run:

- `enabled` false means the location is skipped.
- `status` `pending` means a private location has no agent checked in.

**A check assigned only to a pending location saves successfully and then never
runs.** When checks produce no results, look here before anything else.

## The five types

| `type` | Probes | `target` |
|---|---|---|
| `http` | An endpoint, with assertions on status, headers, body | URL |
| `tcp` | A port accepts a connection | `host:port` |
| `tls` | A certificate, usually expiry | `host:port` |
| `ssh` | An SSH endpoint | `host:port` |
| `browser` | A scripted journey in a real browser | Starting URL |

## `config` is a JSON passthrough

The five types share almost nothing, so type-specific settings live in `config`
as a JSON document rather than five sets of modelled blocks. Use `jsonencode`:

```hcl
config = jsonencode({
  method        = "GET"
  expect_status = 200
  timeout_ms    = 10000
})
```

The provider compares `config` as JSON, not text, so key reordering and
server-side defaults do not read as drift. A check with no type-specific
settings sends `{}` and the provider keeps the attribute null rather than
writing `{}` back, which would be an inconsistent result after apply.

## The check budget

This is the rule that surprises people.

**A single run has a fixed ceiling that does not come from the schedule.**
`ZO_SYNTHETICS_MAX_CHECK_BUDGET_SECS`, 840 seconds by default. It sits below
`ZO_SYNTHETICS_JOB_LEASE_SECS` (900) so a run that finishes at the limit still
has time to report before the reaper assumes the probe died.

Worst case is computed at save time, not measured:

```
combos x (attempts x per_attempt + retries x wait_before_retry_secs)

  attempts     retries + 1
  per_attempt  config.timeout_ms, or config.journey_budget_ms for browser
  combos       browser/device combinations; 1 for non-browser
```

Over budget, the write is refused:

```
validation: config: this check needs up to 15m per run, which is over the 14m
check budget. To fix it, lower retries below 2, or shorten the run with
config.journey_budget_ms
Detail: 1 browser/device combo(s) x 3 attempt(s) x 5m each
```

Three levers, in the order the message lists them: fewer `retries`, a shorter
`config.journey_budget_ms`, fewer `browser_devices`. **Combos multiply
everything**, so adding a second device to a journey that already fits can push
it over on its own.

Related bounds enforced at write time:

- `retries`: 0 to 3 for network checks, 0 to 2 for browser. The budget usually
  bites first.
- `wait_before_retry_secs`: 0 to 300. Defaults to 5, matching the server.
- `config.timeout_ms` for network checks: 1,000 to 300,000.

## Browser journeys

Record a journey in the UI and export it. Hand-writing one is possible and the
shape below is worth knowing for review, but the locator candidate lists that
make a journey survive markup changes come out of the recorder.

```hcl
config = jsonencode({
  steps = [
    { id = "s1", action = "navigate", url = "https://app.example.com/login" },
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
      id        = "s3"
      action    = "assert"
      locator   = { candidates = [{ kind = "css", value = "#dashboard" }] }
      assertion = { kind = "element_visible" }
    },
  ]
  browser_devices = [{ browser = "chromium", device = "desktop" }]
})
```

Note `$${username}` in HCL: a literal `${` would be Terraform interpolation.

Rules enforced on save:

- **The first step must be `navigate`.**
- Every step needs an `id`, unique within the journey. Self-healing keys off it,
  so keep ids stable across edits rather than renumbering.
- Actions: `navigate`, `click`, `hover`, `fill`, `press`, `select`, `check`,
  `uncheck`, `upload`, `assert`.
- `fill` and `select` require `value`.
- `assert` requires `assertion`. Kinds: `element_visible`,
  `element_not_visible`, `element_text`, `url_matches`, `page_title`,
  `element_attribute`. The two visibility kinds take no `expected`;
  `url_matches` and `page_title` describe the page and take no locator.
- `locator.candidates` needs at least one entry, max 8. Kinds:
  `test_attribute`, `role`, `text`, `css`, `xpath`.
- `button` if set must be `left`, `middle` or `right`; `click_count` 1 to 3.
- Max 50 steps, and the steps JSON must stay under 256KB.

`locator.candidates` is ordered, best first, and the probe tries each in turn.
Put a stable `test_attribute` first and a brittle `css` or `xpath` last.

**A journey with no assert step is not a test.** It passes as long as nothing
throws, including when a login lands on an error page.

## Credentials

```hcl
auth {
  type        = "secret"
  secret_name = "synthetic-login"
}
```

`auth` is a flat block with a `type` discriminator rather than one block per
kind. That is deliberate: a `SingleNestedBlock` combined with `dynamic` produces
a present object full of unknowns rather than a null, which makes validation
that checks for absence fire wrongly.

| `type` | Fields | In state? |
|---|---|---|
| `basic` | `username`, `password` | Yes |
| `bearer` | `token` | Yes |
| `secret` | `secret_name` | No |

Prefer `secret`: it names a credential already in OpenObserve, so nothing
sensitive enters state.

Parameterise the rest instead of hardcoding:

```hcl
variable {
  name   = "password"
  value  = var.synthetic_password
  secure = true
}

cookie {
  name   = "consent"
  value  = "accepted"
  domain = "app.example.com"
}
```

A `secure` variable comes back from the server with its value emptied and only
`example` populated. The provider therefore fills `auth`, `cookie` and
`variable` from the server **only when the model has none**, which is the import
case, and otherwise keeps configured values. The consequence worth stating to a
user: **drift in a secure value cannot be detected.** A UI edit goes unnoticed.

## Browser-only attributes

`collect_rum_data` and `session_replay` are rejected during `plan` on any other
type, rather than being silently ignored by the server.

## Scheduling

`frequency_type` is `seconds`, `minutes`, `hours`, `days`, `weeks`, `months` or
`cron`. With `cron`, set `cron` and optionally `timezone`; the expression is
**6-field** (seconds first), and the server rewrites a leading `*` to the
current second so every run does not fire at once.

## Folders, enabling, moving

Synthetics follow the alert pattern exactly:

- The destination folder comes from a `?folder=` query parameter. A folder in
  the body is ignored, because the permission gate reads the query.
- `enabled` has its own endpoint. Writing it into the update body does nothing.
- Moving between folders is a separate PATCH.

The provider handles all three. The one place it leaks: an **update** body must
still carry `folder_id`, or SQLite rejects it with `FOREIGN KEY constraint
failed (787)`.

## Import

By id, not name:

```console
$ terraform import openobserve_synthetic.api_up default/3Ip9aYhgjr5Ozj5bzb58deBuE2s
```

Find ids with:

```hcl
data "openobserve_synthetics" "all" {}

output "ids" {
  value = [
    for s in data.openobserve_synthetics.all.synthetics :
    { name = s.name, id = s.synthetic_id, folder = s.folder_id }
  ]
}
```

Note the list rather than a map keyed by name. **Check names are unique per
folder, not per organization**, so two folders may each hold an `api_up` and a
name-keyed map fails to build with `Two different items produced the key`.

An imported check brings cookies and variables with it, but `secure` values
arrive empty. Fill them in from your own source of truth before the first apply.
