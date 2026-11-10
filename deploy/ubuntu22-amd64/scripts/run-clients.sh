#!/usr/bin/env bash
# Run a supervised group of simulated clients on this machine.
source "$(dirname -- "$0")/common.sh"
usage() {
 printf 'Usage: %s -NUMBER [client.env]\n       %s -n NUMBER [client.env]\nExample: %s -10\n' "$0" "$0" "$0"
}
case ${1:-} in
 -h|--help) usage; exit 0 ;;
 -n|--count) [[ $# -ge 2 ]] || { usage >&2; exit 2; }; count=$2; shift 2 ;;
 -[0-9]*) count=${1#-}; shift ;;
 *) usage >&2; exit 2 ;;
esac
[[ $count =~ ^[1-9][0-9]{0,3}$ ]] && (( count <= 1000 )) || fail 'Client count must be an integer from 1 to 1000 (for example -10).'
[[ $# -le 1 ]] || { usage >&2; exit 2; }
require_target
client_config "${1:-$PACKAGE_ROOT/config/client.env}"
[[ -x $PACKAGE_ROOT/bin/pq-heal-client ]] || fail 'Client binary missing or not executable.'
sensor=${PQHEAL_SENSOR_TYPE:-heart_rate}
case $sensor in heart_rate|person_temperature|ambient_temperature|motion) ;; *) fail "Invalid sensor type: $sensor";; esac
results=${RESULTS_DIR:-$PACKAGE_ROOT/results}
mkdir -p -- "$results"
run=$(mktemp -d "$results/clients-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
# Unique across simultaneous runs and machines; no shared source config edits.
prefix="$(basename "$run")"
pids=()
cleanup() {
 local rc=$? pid active attempt
 trap - EXIT
 trap '' INT TERM HUP
 for pid in "${pids[@]}"; do kill -TERM "$pid" 2>/dev/null || true; done
 # Bound shutdown even if a child hangs, then reap every child.
 for ((attempt=0;attempt<50;attempt++)); do
  active=false
  for pid in "${pids[@]}"; do if kill -0 "$pid" 2>/dev/null; then active=true; break; fi; done
  [[ $active == true ]] || break
  sleep 0.1
 done
 for pid in "${pids[@]}"; do
  if kill -0 "$pid" 2>/dev/null; then kill -KILL "$pid" 2>/dev/null || true; fi
  wait "$pid" 2>/dev/null || true
 done
 printf '%s\n' "$rc" > "$run/EXIT_STATUS"
 printf '\nClients stopped. Logs: %s\n' "$run"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP
printf 'index\tpid\tclient_id\tsubject_id\ttype\tlog\n' > "$run/clients.tsv"
for ((i=1;i<=count;i++)); do
 id="$prefix-$i"
 subject="$prefix-subject-$i"
 logfile="$run/client-$i.log"
 "$PACKAGE_ROOT/bin/pq-heal-client" \
  -server="$PQHEAL_SERVER_URL" -encrypt -key-pin="$PQHEAL_KEY_PIN" \
  -id="$id" -subject-id="$subject" -type="$sensor" \
  -frequency="${PQHEAL_FREQUENCY:-5s}" -heartbeat=5s \
  </dev/null >"$logfile" 2>&1 &
 pid=$!
 pids+=("$pid")
 printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$i" "$pid" "$id" "$subject" "$sensor" "client-$i.log" >> "$run/clients.tsv"
done
printf 'Started %s %s client processes. Ctrl+C stops all clients.\nLogs: %s\nCheck client logs for successful delivery.\n' "$count" "$sensor" "$run"
# Observe exits without restarting clients or hiding failures. Polling also works
# with older Bash versions used by the development-host lifecycle tests.
while true; do
 for pid in "${pids[@]}"; do
  if ! kill -0 "$pid" 2>/dev/null; then
   if wait "$pid"; then status=0; else status=$?; fi
   printf 'A client exited unexpectedly (status %s); stopping the group. Inspect the client logs.\n' "$status" >&2
   exit 1
  fi
 done
 sleep 1
done
