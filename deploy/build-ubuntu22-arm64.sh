#!/usr/bin/env bash
# Build a self-contained deployment bundle from this working tree, not only HEAD.
set -euo pipefail
umask 022
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
out=${OUT:-$repo/dist}
mkdir -p "$out"
out=$(cd "$out" && pwd)
bundle="$out/pq-heal-ubuntu22.04-arm64"
[[ ! -e $bundle ]] || { echo "Output exists: $bundle; use a new OUT directory." >&2; exit 1; }
mkdir -p "$bundle/bin" "$bundle/model" "$bundle/config" "$bundle/scripts" "$bundle/assets"
# Copy only distribution inputs, never locally generated configuration or state.
cp deploy/ubuntu22-arm64/config/*.example "$bundle/config/"
cp deploy/ubuntu22-arm64/scripts/*.sh deploy/ubuntu22-arm64/scripts/*.py "$bundle/scripts/"
cp deploy/ubuntu22-arm64/assets/*.csv.gz "$bundle/assets/"
cp deploy/ubuntu22-arm64/GUIDE.md deploy/ubuntu22-arm64/REFERENCE.md deploy/ubuntu22-arm64/VALIDATION.md deploy/ubuntu22-arm64/pq-heal-hub.service "$bundle/"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for entry in 'server:pq-heal-server' 'client:pq-heal-client' 'kpibench:pq-heal-kpibench' 'd22bench:pq-heal-detection-bench'; do
 cmd=${entry%%:*};name=${entry#*:}
 CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$bundle/bin/$name" "./cmd/$cmd"
done
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -trimpath -buildvcs=false -ldflags='-s -w' -o "$bundle/bin/pq-heal-crypto-bench" ./internal/secure
# Fit deterministically on the supplied training split. No evaluation data is fitted.
gzip -dc "$bundle/assets/ad_train.csv.gz" > "$work/ad_train.csv"
gzip -dc "$bundle/assets/ad_eval.csv.gz" > "$work/ad_eval.csv"
CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$work/detection-bench" ./cmd/d22bench
"$work/detection-bench" -train="$work/ad_train.csv" -eval="$work/ad_eval.csv" \
 -model-out="$bundle/model/anomaly-model.json" -json-out="$bundle/model/build-host-evaluation.json" > "$bundle/model/build-host-evaluation.txt"
{
 printf 'Target: Ubuntu 22.04 LTS Linux ARM64/aarch64; CGO_ENABLED=0\nBuilt UTC: '
 date -u +%Y-%m-%dT%H:%M:%SZ
 go version
 printf 'Base revision: ';git rev-parse HEAD
 printf '\nWorking-tree status (included changes are in source.tar.gz):\n';git status --short
 printf '\nModules:\n';go list -m all
 printf '\nBuild-host model metrics are not Ubuntu ARM64 measurements.\n'
} > "$bundle/BUILD-INFO.txt"
# Explicit allowlist prevents credentials, telemetry and Git/editor state leaking.
COPYFILE_DISABLE=1 tar --exclude='.DS_Store' --exclude='__pycache__' --exclude='deploy/ubuntu22-*/state' --exclude='deploy/ubuntu22-*/results' --exclude='client-setup.tar.gz' --exclude='*.env' --exclude='*.key' --exclude='*.crt' --exclude='*.csr' --exclude='*.srl' --exclude='*.ext' \
 -czf "$bundle/source.tar.gz" go.mod go.sum README.md cmd internal deploy
chmod +x "$bundle/scripts/"*.sh "$bundle/bin/"*
python3 - "$bundle" <<'PY'
from pathlib import Path
import hashlib,sys
root=Path(sys.argv[1])
files=sorted(p for p in root.rglob('*') if p.is_file() and p.name!='SHA256SUMS')
(root/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.relative_to(root).as_posix()+'\n' for p in files))
PY
COPYFILE_DISABLE=1 tar -C "$out" -czf "$bundle.tar.gz" "$(basename "$bundle")"
python3 - "$bundle.tar.gz" <<'PY'
import hashlib,pathlib,sys
p=pathlib.Path(sys.argv[1]);p.with_name(p.name+'.sha256').write_text(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n')
PY
printf 'Ready: %s.tar.gz\n' "$bundle"
