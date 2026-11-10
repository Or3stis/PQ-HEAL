#!/usr/bin/env bash
# Export only a client credential, public CA and trusted message-key pin.
source "$(dirname -- "$0")/common.sh"
require_target
[[ $EUID == 0 ]] || fail 'Run with sudo.'
load_config /etc/pq-heal/server.env
# Load URL and CA from the test setup, but always retain the installed API key.
installed_key=$PQHEAL_API_KEY
load_config "$PACKAGE_ROOT/config/client.env"
PQHEAL_API_KEY=$installed_key
export PQHEAL_API_KEY
pin=
for ((i=0;i<30;i++)); do
 if pin=$("$PACKAGE_ROOT/bin/pq-heal-kpibench" -mode=pin -server="$PQHEAL_SERVER_URL" 2>/dev/null); then break; fi
 sleep 1
done
[[ $pin =~ ^[0-9a-f]{64}$ ]] || fail 'Cannot retrieve trusted pin. Check service logs, address and TLS; then rerun export-client.sh.'
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -m 700 "$work/config"
cp "$SSL_CERT_FILE" "$work/config/lab-ca.crt"
{
 printf 'PQHEAL_API_KEY=%q\nPQHEAL_SERVER_URL=%q\nPQHEAL_KEY_PIN=%q\n' "$PQHEAL_API_KEY" "$PQHEAL_SERVER_URL" "$pin"
 # Resolve against the package on the receiving machine, not the server path.
 printf 'SSL_CERT_FILE="${PACKAGE_ROOT}/config/lab-ca.crt"\n'
 printf 'PQHEAL_CLIENT_ID=test-heart-01\nPQHEAL_SUBJECT_ID=test-subject-01\nPQHEAL_SENSOR_TYPE=heart_rate\nPQHEAL_FREQUENCY=5s\n'
} > "$work/config/client.env"
# Keep a ready-to-use local client config for server-side maintenance tests too.
cp "$work/config/client.env" "$PACKAGE_ROOT/config/client.env"
chmod 600 "$PACKAGE_ROOT/config/client.env"
COPYFILE_DISABLE=1 tar -C "$work" -czf "$PACKAGE_ROOT/client-setup.tar.gz" config/client.env config/lab-ca.crt
chmod 600 "$PACKAGE_ROOT/client-setup.tar.gz"
if [[ -n ${SUDO_UID:-} && -n ${SUDO_GID:-} ]]; then
 chown "$SUDO_UID:$SUDO_GID" "$PACKAGE_ROOT/client-setup.tar.gz"
fi
printf 'Created %s/client-setup.tar.gz (contains the shared API credential; transfer privately).\n' "$PACKAGE_ROOT"
