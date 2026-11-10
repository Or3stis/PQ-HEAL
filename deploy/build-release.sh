#!/usr/bin/env bash
# Build the PQ-HEAL Home Hub release binaries for native edge deployment.
#
# The output is a statically linked executable with no runtime dependencies:
# no interpreter, no shared libraries, no container runtime. It is copied to the
# edge node and run under systemd.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT=${OUT:-dist}
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
TARGETS=${TARGETS:-"linux/amd64 linux/arm64"}

mkdir -p "$OUT"

for target in $TARGETS; do
    goos=${target%%/*}
    goarch=${target##*/}
    name="pq-heal-server-${goos}-${goarch}"

    # CGO_ENABLED=0 gives a static binary that does not depend on the host libc,
    # so one build runs on any distribution of the same architecture.
    # -trimpath removes build-host paths; -s -w drop the symbol and DWARF tables.
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -ldflags="-s -w" -o "$OUT/$name" ./cmd/server

    ( cd "$OUT" && sha256sum "$name" > "$name.sha256" )
    echo "built $OUT/$name ($VERSION)"
done

echo
echo "Checksums:"
cat "$OUT"/*.sha256
