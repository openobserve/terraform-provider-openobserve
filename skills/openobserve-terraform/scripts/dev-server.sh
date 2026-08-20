#!/usr/bin/env bash
#
# Start a throwaway OpenObserve for verifying a Terraform change against a real
# server. A configuration that applies once is not a working configuration; the
# lifecycle at the bottom of this file is what actually catches problems.
#
#   ./dev-server.sh start     start the container and print the exports
#   ./dev-server.sh stop      stop and remove it, discarding all data
#   ./dev-server.sh env       print the exports for an already-running instance
#
set -euo pipefail

NAME="${OO_DEV_NAME:-oo-tf-dev}"
PORT="${OO_DEV_PORT:-5080}"
EMAIL="${OO_DEV_EMAIL:-root@example.com}"
PASSWORD="${OO_DEV_PASSWORD:-Complexpass#123}"
IMAGE="${OO_DEV_IMAGE:-openobserve/openobserve:latest}"

print_env() {
  cat <<EOF
export OPENOBSERVE_ENDPOINT="http://localhost:${PORT}"
export OPENOBSERVE_USERNAME="${EMAIL}"
export OPENOBSERVE_PASSWORD='${PASSWORD}'
export OPENOBSERVE_ORG_ID="default"
EOF
}

case "${1:-start}" in
  start)
    if docker ps -a --format '{{.Names}}' | grep -qx "$NAME"; then
      echo "Container ${NAME} already exists. Run '$0 stop' first." >&2
      exit 1
    fi

    docker run -d --name "$NAME" \
      -p "${PORT}:5080" \
      -e ZO_ROOT_USER_EMAIL="$EMAIL" \
      -e ZO_ROOT_USER_PASSWORD="$PASSWORD" \
      "$IMAGE" >/dev/null

    printf 'waiting for OpenObserve'
    for _ in $(seq 1 60); do
      if curl -fsS -o /dev/null -m 2 "http://localhost:${PORT}/healthz" 2>/dev/null; then
        echo " ready"
        print_env
        exit 0
      fi
      printf '.'
      sleep 1
    done
    echo
    echo "Timed out. Check: docker logs ${NAME}" >&2
    exit 1
    ;;

  stop)
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    echo "Removed ${NAME}. All data is gone."
    ;;

  env)
    print_env
    ;;

  *)
    echo "usage: $0 {start|stop|env}" >&2
    exit 2
    ;;
esac

# ---------------------------------------------------------------------------
# The lifecycle worth running against it
# ---------------------------------------------------------------------------
#
# A change is not verified until all five pass:
#
#   1. terraform apply                       succeeds
#   2. terraform apply                       reports no changes
#   3. terraform plan                        reports no changes
#   4. terraform state rm <addr>
#      terraform import <addr> <id>
#      terraform apply                       converges
#      terraform plan                        reports no changes
#   5. terraform destroy                     succeeds
#      terraform destroy                     is not an error
#
# Most bugs surface at steps 3 and 4, not step 1.
#
# Use -parallelism=1 against this server. SQLite "code: 517" is write
# contention on a single-node metastore, not an API error.
#
# Composite alerts also need -parallelism=1 when several are created at once:
# composite writes serialize on a per-organization graph lock and otherwise
# return composite_graph_lock_unavailable.
#
# Enterprise features (roles, groups) need a different image and:
#   -e ZO_OPENFGA_ENABLED=true
# Without it those resources return a diagnostic saying the feature is
# unavailable, which is the expected behaviour on open source.
