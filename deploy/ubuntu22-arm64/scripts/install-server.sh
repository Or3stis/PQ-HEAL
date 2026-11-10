#!/usr/bin/env bash
source "$(dirname -- "$0")/common.sh"
require_target
[[ $EUID == 0 ]] || fail 'Run with sudo.'
[[ -f /sys/fs/cgroup/cgroup.controllers ]] || fail 'Enable cgroup v2 before installing.'
[[ ! -e /opt/pq-heal && ! -e /etc/pq-heal/server.env ]] || fail 'Existing deployment: use the documented upgrade procedure.'
load_config "${1:?Usage: sudo scripts/install-server.sh /absolute/path/server.env}"
: "${PQHEAL_BIND:?Set PQHEAL_BIND}"
: "${PQHEAL_TLS_CERT:?Set PQHEAL_TLS_CERT}"
: "${PQHEAL_TLS_KEY:?Set PQHEAL_TLS_KEY}"
[[ -r $PQHEAL_TLS_CERT && -r $PQHEAL_TLS_KEY ]] || fail 'TLS files unreadable.'
(cd "$PACKAGE_ROOT" && sha256sum -c SHA256SUMS)
getent group pqheal >/dev/null || groupadd --system pqheal
id pqheal >/dev/null 2>&1 || useradd --system --gid pqheal --no-create-home --shell /usr/sbin/nologin pqheal
install -d -m 0755 /opt/pq-heal /opt/pq-heal/bin /opt/pq-heal/scripts /opt/pq-heal/model
install -m 0755 "$PACKAGE_ROOT/bin/pq-heal-server" /opt/pq-heal/bin/
install -m 0755 "$PACKAGE_ROOT/scripts/common.sh" "$PACKAGE_ROOT/scripts/run-server.sh" /opt/pq-heal/scripts/
install -m 0644 "$PACKAGE_ROOT/model/anomaly-model.json" /opt/pq-heal/model/
install -d -o root -g pqheal -m 0750 /etc/pq-heal
install -d -o pqheal -g pqheal -m 0750 /var/lib/pq-heal
install -o root -g pqheal -m 0640 "$PQHEAL_TLS_CERT" /etc/pq-heal/server.crt
install -o root -g pqheal -m 0640 "$PQHEAL_TLS_KEY" /etc/pq-heal/server.key
{
 printf 'PQHEAL_API_KEY=%q\nPQHEAL_BIND=%q\nPQHEAL_PORT=%q\n' "$PQHEAL_API_KEY" "$PQHEAL_BIND" "${PQHEAL_PORT:-9000}"
 printf 'PQHEAL_TLS_CERT=/etc/pq-heal/server.crt\nPQHEAL_TLS_KEY=/etc/pq-heal/server.key\nPQHEAL_STATE_DIR=/var/lib/pq-heal\nPQHEAL_MODEL=/opt/pq-heal/model/anomaly-model.json\n'
} > /etc/pq-heal/server.env
chown root:pqheal /etc/pq-heal/server.env
chmod 0640 /etc/pq-heal/server.env
install -m 0644 "$PACKAGE_ROOT/pq-heal-hub.service" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now pq-heal-hub
systemctl --no-pager --full status pq-heal-hub
