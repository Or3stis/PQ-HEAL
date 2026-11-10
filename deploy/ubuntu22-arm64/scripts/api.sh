#!/usr/bin/env bash
# Authenticated maintenance calls; API credentials never appear in curl argv.
source "$(dirname -- "$0")/common.sh"
require_target
client_config "${1:-$PACKAGE_ROOT/config/client.env}"
method=${2:-GET}; endpoint=${3:-/clients}
case "$method $endpoint" in
 'GET /clients'|'GET /keys'|'GET /keys/all'|'GET /health'|'POST /keys/rotate') ;;
 *) fail 'Allowed: GET /clients, /keys, /keys/all, /health; POST /keys/rotate';;
esac
[[ $PQHEAL_API_KEY =~ ^[a-zA-Z0-9_+/=-]+$ ]] || fail 'Unsupported credential characters for curl configuration.'
args=(--silent --show-error --fail-with-body --connect-timeout 5 --max-time 15 --request "$method")
[[ -z ${SSL_CERT_FILE:-} ]] || args+=(--cacert "$SSL_CERT_FILE")
printf 'header = "Authorization: Bearer %s"\n' "$PQHEAL_API_KEY" | curl --config - "${args[@]}" "$PQHEAL_SERVER_URL$endpoint"
printf '\n'
