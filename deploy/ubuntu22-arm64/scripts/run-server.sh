#!/usr/bin/env bash
source "$(dirname -- "$0")/common.sh"
require_target
load_config "${1:-$PACKAGE_ROOT/config/server.env}"
: "${PQHEAL_BIND:?Set the server data-plane IP}"
: "${PQHEAL_TLS_CERT:?Set TLS certificate path}"
: "${PQHEAL_TLS_KEY:?Set TLS key path}"
state=${PQHEAL_STATE_DIR:-$PACKAGE_ROOT/state}
model=${PQHEAL_MODEL:-$PACKAGE_ROOT/model/anomaly-model.json}
[[ -s $model && -r $PQHEAL_TLS_CERT && -r $PQHEAL_TLS_KEY ]] || fail 'Model or TLS files unavailable.'
mkdir -p -- "$state"
exec "$PACKAGE_ROOT/bin/pq-heal-server" \
 -bind="$PQHEAL_BIND" -port="${PQHEAL_PORT:-9000}" \
 -tls-cert="$PQHEAL_TLS_CERT" -tls-key="$PQHEAL_TLS_KEY" -model="$model" \
 -client-port-log="$state/client-ports.csv" -log="$state/data.csv" -key-store="$state/server-keys.json" \
 -max-clients=1000 -evict-after=15m -alive-window=10s \
 -rate-limit-rps=50 -rate-limit-burst=100 -access-log -log-format=json
