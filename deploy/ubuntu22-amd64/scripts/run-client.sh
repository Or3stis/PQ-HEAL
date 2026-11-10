#!/usr/bin/env bash
source "$(dirname -- "$0")/common.sh"
require_target
client_config "${1:-$PACKAGE_ROOT/config/client.env}"
exec "$PACKAGE_ROOT/bin/pq-heal-client" \
 -server="$PQHEAL_SERVER_URL" -encrypt -key-pin="$PQHEAL_KEY_PIN" \
 -id="${PQHEAL_CLIENT_ID:-test-heart-01}" -subject-id="${PQHEAL_SUBJECT_ID:-test-subject-01}" \
 -type="${PQHEAL_SENSOR_TYPE:-heart_rate}" -frequency="${PQHEAL_FREQUENCY:-5s}" -heartbeat=5s
