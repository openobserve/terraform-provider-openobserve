# Functions are imported as {org_id}/{name}.
terraform import openobserve_function.redact_email default/redact_email

# The server appends a trailing `.` to a VRL body that does not end in one, so
# an import has no configured spelling to preserve and the first plan shows that
# reconciling. Applying converges and changes nothing server-side.
