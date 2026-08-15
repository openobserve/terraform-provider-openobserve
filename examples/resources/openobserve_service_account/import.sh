# Service accounts are imported as {org_id}/{email}.
# The API token cannot be recovered, so `token` is empty after an import;
# change `rotate_token` to issue a fresh one.
terraform import openobserve_service_account.ci default/ci-pipeline@example.com
