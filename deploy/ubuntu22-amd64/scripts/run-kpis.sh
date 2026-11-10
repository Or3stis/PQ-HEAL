#!/usr/bin/env bash
source "$(dirname -- "$0")/common.sh"
require_target
experiment=${1:-help}
case $experiment in
 verify|remote|local|detection|crypto|host|privacy|recovery) ;;
 *) printf 'Usage: %s {verify|remote|local|detection|crypto|host|privacy|recovery} [client.env]\n' "$0"; exit 2;;
esac
results=${RESULTS_DIR:-$PACKAGE_ROOT/results}
mkdir -p -- "$results"
run=$(mktemp -d "$results/${experiment}-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")
trap 'rc=$?; printf "%s\n" "$rc" > "$run/EXIT_STATUS"; printf "Evidence: %s (exit %s)\n" "$run" "$rc"' EXIT
{
 date -u --iso-8601=seconds
 uname -a
 cat /etc/os-release
 lscpu
 printf '\nClock synchronization:\n'
 timedatectl status || true
 printf '\nBuild:\n'
 cat "$PACKAGE_ROOT/BUILD-INFO.txt"
 printf '\nArtifact hashes:\n'
 sha256sum "$PACKAGE_ROOT/bin/"* "$PACKAGE_ROOT/model/anomaly-model.json" "$PACKAGE_ROOT/assets/"*.gz
} > "$run/environment.txt"
case $experiment in
 verify|remote)
  client_config "${2:-$PACKAGE_ROOT/config/client.env}"
  "$PACKAGE_ROOT/bin/pq-heal-kpibench" -mode="$experiment" -server="$PQHEAL_SERVER_URL" -key-pin="$PQHEAL_KEY_PIN" \
   -samples="${SAMPLES:-1000}" -interval="${INTERVAL:-50ms}" -out="$run/data" 2>&1 | tee "$run/run.log"
  ;;
 local)
  "$PACKAGE_ROOT/bin/pq-heal-kpibench" -mode=local -model="$PACKAGE_ROOT/model/anomaly-model.json" \
   -samples="${SAMPLES:-1000}" -interval=0 -out="$run/data" 2>&1 | tee "$run/run.log"
  ;;
 detection)
  gzip -dc "$PACKAGE_ROOT/assets/ad_train.csv.gz" > "$run/ad_train.csv"
  gzip -dc "$PACKAGE_ROOT/assets/ad_eval.csv.gz" > "$run/ad_eval.csv"
  "$PACKAGE_ROOT/bin/pq-heal-detection-bench" -train="$run/ad_train.csv" -eval="$run/ad_eval.csv" \
   -model="$PACKAGE_ROOT/model/anomaly-model.json" -json-out="$run/detection.json" 2>&1 | tee "$run/run.log"
  ;;
 crypto)
  "$PACKAGE_ROOT/bin/pq-heal-crypto-bench" -test.run=TestReportEnvelopeSizes -test.v \
   -test.bench=. -test.benchmem -test.benchtime=1s -test.count=3 2>&1 | tee "$run/run.log"
  ;;
 host)
  pid=$(systemctl show pq-heal-hub -p MainPID --value)
  [[ $pid =~ ^[1-9][0-9]*$ ]] || fail 'Start pq-heal-hub first.'
  systemctl show pq-heal-hub -p CPUQuotaPerSecUSec -p MemoryMax -p MemoryHigh -p TasksMax -p ControlGroup -p NRestarts > "$run/service.txt"
  python3 "$PACKAGE_ROOT/scripts/sample-host.py" "$pid" "${DURATION_SECONDS:-60}" > "$run/resources.csv"
  ;;
 privacy)
  [[ $EUID == 0 ]] || fail 'Use sudo for installation permission evidence.'
  stat -c '%a %U %G %n' /etc/pq-heal/server.env /etc/pq-heal/server.key /var/lib/pq-heal/data.csv /var/lib/pq-heal/client-ports.csv /var/lib/pq-heal/server-keys.json > "$run/permissions.txt"
  for f in /var/lib/pq-heal/data.csv /var/lib/pq-heal/client-ports.csv /var/lib/pq-heal/server-keys.json; do
   [[ $(stat -c '%a' "$f") == 600 ]] || fail "$f should have mode 600."
  done
  systemctl show pq-heal-hub -p User -p Group -p NoNewPrivileges -p ProtectSystem -p UMask > "$run/confinement.txt"
  printf 'TECHNICAL EVIDENCE ONLY. GDPR compliance requires the manual review in GUIDE.md. No compliance verdict is issued.\n' > "$run/manual-review-required.txt"
  ;;
 recovery)
  [[ $EUID == 0 ]] || fail 'Use sudo; this experiment deliberately restarts the installed service.'
  client_config "${2:-$PACKAGE_ROOT/config/client.env}"
  "$PACKAGE_ROOT/scripts/api.sh" "${2:-$PACKAGE_ROOT/config/client.env}" GET /clients > "$run/clients-before.json"
  sha256sum /var/lib/pq-heal/server-keys.json > "$run/keys-before.sha256"
  start=$(date +%s%N)
  systemctl restart pq-heal-hub
  ready=false
  for ((i=0;i<100;i++)); do
   if "$PACKAGE_ROOT/scripts/api.sh" "${2:-$PACKAGE_ROOT/config/client.env}" GET /health >/dev/null 2>&1; then ready=true; break; fi
   sleep 0.1
  done
  [[ $ready == true ]] || fail 'Server did not recover.'
  end=$(date +%s%N)
  printf 'restart_to_first_successful_probe_ms=%s\nprobe_interval_ms=100\n' "$(((end-start)/1000000))" > "$run/recovery.txt"
  sha256sum /var/lib/pq-heal/server-keys.json > "$run/keys-after.sha256"
  cmp "$run/keys-before.sha256" "$run/keys-after.sha256"
  "$PACKAGE_ROOT/scripts/api.sh" "${2:-$PACKAGE_ROOT/config/client.env}" GET /clients > "$run/clients-after.json"
  python3 - "$run" <<'PY'
import json,sys,pathlib
p=pathlib.Path(sys.argv[1])
a=json.loads((p/'clients-before.json').read_text());b=json.loads((p/'clients-after.json').read_text())
expected={x['id'] for x in a if x.get('has_value')}
actual={x['id'] for x in b}
missing=sorted(expected-actual)
(p/'registry-recovery.json').write_text(json.dumps({'persisted_clients_expected':len(expected),'missing':missing,'pass':not missing},indent=2)+'\n')
if not expected: sys.exit('No persisted clients: run verification before recovery.')
if missing: sys.exit('Persisted clients did not recover.')
PY
  ;;
esac
