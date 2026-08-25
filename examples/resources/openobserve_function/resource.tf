# A VRL transform. The server compiles the body when it is saved, so a syntax
# error fails `terraform apply` rather than quietly breaking a pipeline later.
resource "openobserve_function" "redact_email" {
  name = "redact_email"

  function = <<-VRL
    .email = "[redacted]"
    .redacted_at = now()
  VRL
}

# VRL is the default. A JavaScript transform sets language explicitly, and the
# server compiles it as JS.
resource "openobserve_function" "enrich" {
  name     = "enrich_geo"
  language = "js"

  function = <<-JS
    function process(row) {
      row.region = row.country === "US" ? "amer" : "intl";
      return row;
    }
  JS
}

# Functions are referenced by name from a pipeline's function node. Using the
# reference rather than a literal string is what tells Terraform to create the
# function first, and to destroy the pipeline before it: the server refuses to
# delete a function a pipeline still uses.
