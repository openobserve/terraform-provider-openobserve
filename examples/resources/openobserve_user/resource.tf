resource "openobserve_user" "alice" {
  org_id     = "default"
  email      = "alice@example.com"
  first_name = "Alice"
  last_name  = "Smith"
  role       = "editor"
  password   = "Initialpass#123" # Change after first login
}
