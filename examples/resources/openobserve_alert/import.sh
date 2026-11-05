# Alerts are imported as {org_id}/{alert_id} or {org_id}/{folder_id}/{alert_id}.
# Find the alert ID with the openobserve_alerts data source or the UI URL.
# Either form reads back the folder the alert lives in; naming the folder only
# saves the provider a search of the alert folders.
terraform import openobserve_alert.high_error_rate default/2fXkZ8QlmNbYcV1pR3sT
terraform import openobserve_alert.high_error_rate default/team_alerts/2fXkZ8QlmNbYcV1pR3sT
