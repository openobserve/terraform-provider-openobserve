resource "openobserve_stream" "app_logs" {
  name        = "app_logs"
  stream_type = "logs"
}

resource "openobserve_stream" "app_logs_clean" {
  name        = "app_logs_clean"
  stream_type = "logs"
}

resource "openobserve_function" "redact_email" {
  name = "redact_email"

  function = <<-VRL
    .email = "[redacted]"
  VRL
}

# A pipeline is a graph: records enter from the source stream, flow along edges
# through nodes that transform them, and land in an output.
#
# Note that `function_name` references the function resource rather than naming
# it as a string. That reference is what tells Terraform to create the function
# before the pipeline, and to destroy the pipeline before the function. The
# server refuses to delete a function a pipeline still uses, so a literal string
# here would produce a destroy that fails.
resource "openobserve_pipeline" "redact" {
  name        = "redact_pii"
  description = "Strip email addresses before they land"
  stream_name = openobserve_stream.app_logs.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.app_logs.name
  }

  node {
    id            = "redact"
    type          = "function"
    function_name = openobserve_function.redact_email.name
  }

  node {
    id          = "out"
    type        = "stream"
    stream_name = openobserve_stream.app_logs_clean.name
  }

  # Edges are multi-line blocks. HCL allows only one argument on a single line,
  # so `edge { from = "a", to = "b" }` is a syntax error.
  edge {
    from = "in"
    to   = "redact"
  }

  edge {
    from = "redact"
    to   = "out"
  }
}

# Forwarding to somewhere outside OpenObserve uses a remote_stream node and a
# pipeline destination. A stream can source only one realtime pipeline, so this
# one reads from its own stream.
resource "openobserve_stream" "audit" {
  name        = "audit_events"
  stream_type = "logs"
}

resource "openobserve_pipeline_destination" "warehouse" {
  name = "warehouse"
  url  = "https://example.com/ingest"
}

resource "openobserve_pipeline" "ship_audit" {
  name        = "ship_audit"
  stream_name = openobserve_stream.audit.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.audit.name
  }

  node {
    id               = "out"
    type             = "remote_stream"
    destination_name = openobserve_pipeline_destination.warehouse.name
  }

  edge {
    from = "in"
    to   = "out"
  }
}

# A condition node drops records that do not match, so only what matters flows
# on. `conditions` is a JSON document, so build it with jsonencode().
resource "openobserve_stream" "raw" {
  name        = "raw_events"
  stream_type = "logs"
}

resource "openobserve_pipeline" "errors_only" {
  name        = "errors_only"
  stream_name = openobserve_stream.raw.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.raw.name
  }

  node {
    id   = "filter"
    type = "condition"
    # A tree of conditions: `and`, `or` and `not` nest, and the leaves are
    # column/operator/value comparisons. This is the same shape an alert's
    # `custom` conditions take.
    conditions = jsonencode({
      and = [
        { column = "level", operator = "=", value = "error", ignore_case = false },
      ]
    })
  }

  node {
    id          = "out"
    type        = "stream"
    stream_name = openobserve_stream.app_logs_clean.name
  }

  edge {
    from = "in"
    to   = "filter"
  }

  edge {
    from = "filter"
    to   = "out"
  }
}

# The v2 condition format is a flat list read left to right, the way the UI
# builds a filter. Mark it with `version = 2`, the same wrapper an alert's v2
# conditions use. Each item's `logicalOperator` joins it to the item before it,
# AND binds tighter than OR, and a nested group acts as parentheses. This one
# keeps `level contains "err" AND (code = 500 OR code = 503)`, plus anything
# from the pager service in any case.
resource "openobserve_stream" "http" {
  name        = "http_events"
  stream_type = "logs"
}

resource "openobserve_pipeline" "server_errors" {
  name        = "server_errors"
  stream_name = openobserve_stream.http.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.http.name
  }

  node {
    id   = "filter"
    type = "condition"
    conditions = jsonencode({
      version = 2
      conditions = {
        filterType      = "group"
        logicalOperator = "AND"
        conditions = [
          { filterType = "condition", column = "level", operator = "contains", value = "err", logicalOperator = "AND" },
          {
            filterType      = "group"
            logicalOperator = "AND"
            conditions = [
              { filterType = "condition", column = "code", operator = "=", value = 500, logicalOperator = "AND" },
              { filterType = "condition", column = "code", operator = "=", value = 503, logicalOperator = "OR" },
            ]
          },
          { filterType = "condition", column = "service", operator = "=", value = "pager", ignore_case = true, logicalOperator = "OR" },
        ]
      }
    })
  }

  node {
    id          = "out"
    type        = "stream"
    stream_name = openobserve_stream.app_logs_clean.name
  }

  edge {
    from = "in"
    to   = "filter"
  }

  edge {
    from = "filter"
    to   = "out"
  }
}
