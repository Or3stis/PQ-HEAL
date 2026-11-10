#!/usr/bin/env bash
# Fresh test deployment: configure TLS, install service, export client setup.
source "$(dirname -- "$0")/common.sh"
require_target
[[ $EUID == 0 ]] || fail 'Run with sudo: sudo ./scripts/deploy-server.sh SERVER_IP'
host=${1:?Usage: sudo ./scripts/deploy-server.sh SERVER_IP [BIND_IP]}
[[ ! -e /opt/pq-heal && ! -e /etc/pq-heal/server.env ]] || fail 'Already installed. Use export-client.sh to obtain client setup; see REFERENCE.md for upgrades.'
for command in openssl python3 sha256sum systemctl; do
 command -v "$command" >/dev/null || fail "Missing command: $command"
done
[[ -f /sys/fs/cgroup/cgroup.controllers ]] || fail 'cgroup v2 is required.'
(cd "$PACKAGE_ROOT" && sha256sum --quiet -c SHA256SUMS)
"$PACKAGE_ROOT/scripts/setup-lab.sh" "$host" "${2:-$host}"
"$PACKAGE_ROOT/scripts/install-server.sh" "$PACKAGE_ROOT/config/server.env"
"$PACKAGE_ROOT/scripts/export-client.sh"
printf '\nServer ready. Securely transfer client-setup.tar.gz to the client machine.\n'
