#!/usr/bin/env bash
# Creates test-only credentials, including a SAN certificate for the server IP/name.
source "$(dirname -- "$0")/common.sh"
require_target
host=${1:?Usage: setup-lab.sh SERVER_IP_OR_DNS_NAME [BIND_IP]}
bind=${2:-$host}
[[ $host =~ ^[a-zA-Z0-9.-]+$ && $bind =~ ^[0-9.]+$ ]] || fail 'Use an IPv4 address or DNS name plus an IPv4 bind address.'
for f in server.env client.env lab-server.key lab-ca.key; do
 [[ ! -e $PACKAGE_ROOT/config/$f ]] || fail "Existing config/$f: refusing to overwrite credentials."
done
command -v openssl >/dev/null || fail 'Install openssl.'
cd "$PACKAGE_ROOT/config"
openssl req -x509 -newkey rsa:3072 -nodes -sha256 -days 90 -subj '/CN=PQ-HEAL TEST CA' -keyout lab-ca.key -out lab-ca.crt -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign'
openssl req -newkey rsa:3072 -nodes -sha256 -subj "/CN=$host" -keyout lab-server.key -out lab-server.csr
san="DNS:$host"; [[ ! $host =~ ^[0-9.]+$ ]] || san="IP:$host"
printf 'subjectAltName=%s\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\n' "$san" > lab-server.ext
openssl x509 -req -in lab-server.csr -CA lab-ca.crt -CAkey lab-ca.key -CAcreateserial -days 30 -sha256 -extfile lab-server.ext -out lab-server.crt
key=$(openssl rand -hex 32)
{
 printf 'PQHEAL_API_KEY=%q\nPQHEAL_BIND=%q\nPQHEAL_PORT=9000\n' "$key" "$bind"
 printf 'PQHEAL_TLS_CERT=%q\nPQHEAL_TLS_KEY=%q\n' "$PACKAGE_ROOT/config/lab-server.crt" "$PACKAGE_ROOT/config/lab-server.key"
 printf 'PQHEAL_STATE_DIR=%q\nPQHEAL_MODEL=%q\n' "$PACKAGE_ROOT/state" "$PACKAGE_ROOT/model/anomaly-model.json"
} > server.env
{
 printf 'PQHEAL_API_KEY=%q\nPQHEAL_SERVER_URL=%q\n' "$key" "https://$host:9000"
 printf 'SSL_CERT_FILE=%q\nPQHEAL_KEY_PIN=REPLACE_WITH_TRUSTED_PIN\n' "$PACKAGE_ROOT/config/lab-ca.crt"
 printf 'PQHEAL_CLIENT_ID=test-heart-01\nPQHEAL_SUBJECT_ID=test-subject-01\nPQHEAL_SENSOR_TYPE=heart_rate\nPQHEAL_FREQUENCY=5s\n'
} > client.env
chmod 600 ./*.key server.env client.env
printf 'Created test configuration. Start the server, obtain its pin over trusted HTTPS, then edit client.env. Never copy private keys to the client.\n'
