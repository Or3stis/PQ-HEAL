#!/usr/bin/env bash
set -euo pipefail
umask 077
PACKAGE_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
require_target() {
 [[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Requires Linux AMD64/x86_64.'
 [[ -r /etc/os-release ]] || fail 'Missing OS release.'
 local ID VERSION_ID
 source /etc/os-release
 [[ $ID == ubuntu && $VERSION_ID == 22.04 ]] || fail 'This package targets Ubuntu 22.04.'
}
load_config() {
 local path=$1 mode
 [[ -f $path ]] || fail "Missing configuration: $path"
 mode=$(stat -c '%a' "$path")
 (( (8#$mode & 8#007) == 0 )) || fail "Configuration is readable by others: chmod 600 '$path'"
 # This is an operator-owned Bash configuration, never an untrusted download.
 set -a
 source "$path"
 set +a
 [[ -n ${PQHEAL_API_KEY:-} && ${PQHEAL_API_KEY:-} != replace-me ]] || fail 'Set PQHEAL_API_KEY.'
}
client_config() {
 load_config "$1"
 [[ ${PQHEAL_SERVER_URL:-} == https://* ]] || fail 'Set an HTTPS PQHEAL_SERVER_URL.'
 [[ ${PQHEAL_KEY_PIN:-} =~ ^[0-9a-fA-F]{64}$ ]] || fail 'Set a trusted 64-character PQHEAL_KEY_PIN.'
 if [[ -n ${SSL_CERT_FILE:-} ]]; then [[ -r $SSL_CERT_FILE ]] || fail 'Cannot read SSL_CERT_FILE.'; fi
}
