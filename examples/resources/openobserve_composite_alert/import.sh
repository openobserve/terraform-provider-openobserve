# Composite alerts share the alert ID namespace and are imported the same way,
# as {org_id}/{alert_id}.
# Find the ID with the openobserve_alerts data source, filtering on
# alert_type == "composite", or read it from the UI URL.
terraform import openobserve_composite_alert.bad_deploy default/2fXkZ8QlmNbYcV1pR3sT

# An import has no configured expression to preserve, so state holds the
# server's fully parenthesized form. The first plan afterwards shows that as a
# change to your spelling; applying it converges and changes nothing on the
# server.
