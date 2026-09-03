# Synthetic checks are imported as {org_id}/{synthetic_id}. The identifier is
# server-assigned, so find it with the openobserve_synthetics data source.
terraform import openobserve_synthetic.api_up default/2fXkZ8QlmNbYcV1pR3sT

# Credentials are not read back: an imported check has no auth, cookie or
# variable values in state, because the server holds them encrypted. The first
# plan afterwards re-applies whatever the configuration declares.
