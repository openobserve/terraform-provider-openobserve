# Pipelines, VRL functions and pipeline destinations, and the dependency that
# ties them together.
#
# Credentials come from the environment:
#   OPENOBSERVE_ENDPOINT, OPENOBSERVE_USERNAME, OPENOBSERVE_PASSWORD, OPENOBSERVE_ORG_ID

terraform {
  required_providers {
    openobserve = {
      source  = "openobserve/openobserve"
      version = "~> 1.4"
    }
  }
}

provider "openobserve" {}

resource "openobserve_stream" "raw" {
  name        = "pipe_example_raw"
  stream_type = "logs"
}

resource "openobserve_stream" "clean" {
  name        = "pipe_example_clean"
  stream_type = "logs"
}

resource "openobserve_stream" "audit" {
  name        = "pipe_example_audit"
  stream_type = "logs"
}

# --- The transform -----------------------------------------------------------
#
# The server compiles this when it is saved, so a syntax error fails the apply
# rather than surfacing later as a pipeline that quietly stopped working.

resource "openobserve_function" "redact" {
  name = "pipe_example_redact"

  function = <<-VRL
    .email = "[redacted]"
    .processed_by = "terraform"
  VRL
}

# --- Where records can be sent -----------------------------------------------
#
# The URL is resolved when the destination is saved, so the host has to exist by
# the time Terraform applies.

resource "openobserve_pipeline_destination" "warehouse" {
  name = "pipe_example_warehouse"
  url  = "https://example.com/ingest"

  headers = {
    Authorization = "Bearer example-token"
  }
}

# --- stream -> function -> stream --------------------------------------------
#
# `function_name` references the function resource rather than naming it. That
# reference is the whole point: it tells Terraform to create the function first
# and destroy the pipeline first, and the server refuses to delete a function a
# pipeline still uses.

resource "openobserve_pipeline" "redact" {
  name        = "pipe_example_redact_pii"
  description = "Strip email addresses before they land"
  stream_name = openobserve_stream.raw.name

  node {
    id          = "in"
    type        = "stream"
    stream_name = openobserve_stream.raw.name
  }

  node {
    id            = "redact"
    type          = "function"
    function_name = openobserve_function.redact.name
  }

  node {
    id          = "out"
    type        = "stream"
    stream_name = openobserve_stream.clean.name
  }

  # Edges are multi-line: HCL allows only one argument on a single-line block.
  edge {
    from = "in"
    to   = "redact"
  }

  edge {
    from = "redact"
    to   = "out"
  }
}

# --- stream -> remote_stream -------------------------------------------------
#
# A stream can be the source of only one realtime pipeline, so this reads from
# its own stream rather than reusing `raw`.

resource "openobserve_pipeline" "ship" {
  name        = "pipe_example_ship_audit"
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

# --- Inspecting --------------------------------------------------------------

data "openobserve_function" "redact" {
  name       = openobserve_function.redact.name
  depends_on = [openobserve_pipeline.redact]
}

output "function_blocked_by" {
  description = "Pipelines holding this function. Non-empty means a delete will be refused."
  value       = data.openobserve_function.redact.used_by
}

data "openobserve_pipelines" "all" {
  depends_on = [openobserve_pipeline.redact, openobserve_pipeline.ship]
}

# A pipeline's identifier is server-assigned, so this is how to find what an
# import needs.
output "pipeline_ids" {
  value = { for p in data.openobserve_pipelines.all.pipelines : p.name => p.pipeline_id }
}

# io_type is inferred from the edges rather than written by hand.
output "inferred_io_types" {
  value = { for n in openobserve_pipeline.redact.node : n.id => n.io_type }
}
