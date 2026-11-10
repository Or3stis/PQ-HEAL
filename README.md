# PQ Heal

CLI server and client for real-time health telemetry over HTTP written in Go.

## Quickstart

1. Start the server:

```bash
go run ./cmd/server -api-key dev-secret
```

2. Start a client in another terminal:

```bash
go run ./cmd/client -api-key dev-secret
```

3. Enter a client ID, choose a type, and set a data transmission frequency.

## Requirements

- Go 1.26+
- Module dependency: `github.com/cloudflare/circl` (ML-KEM-768)

## Build

Build the server binary for Linux (amd64):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o pq-heal-server ./cmd/server
```

Build the client binary for Linux (amd64):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o pq-heal-client ./cmd/client
```

Run the server (secure default):

```bash
./pq-heal-server -api-key your-secret
```

Run with built-in TLS (recommended whenever `-api-key` is set — see [Transport security](#transport-security-the-api-key-is-sent-in-cleartext-without-tls)):

```bash
./pq-heal-server -api-key your-secret -tls-cert server.crt -tls-key server.key
```

Run for local/testing only without auth:

```bash
./pq-heal-server -allow-unauthenticated
```

## Project layout

- `cmd/server` — server CLI
- `cmd/client` — client CLI
- `internal/server` — server modules
- `internal/client` — client modules
- `internal/protocol` — shared types

## Run the server

```bash
go run ./cmd/server -api-key your-secret
```

Local/testing only (explicitly unauthenticated):

```bash
go run ./cmd/server -allow-unauthenticated
```

Flags:

- `-port` (default `9000`)
- `-bind` (default empty, binds all interfaces; set to an interface address to separate data-plane from management-plane traffic)
- `-log` (default `data.csv`, empty disables logging)
- `-alive-window` (default `10s`)
- `-max-clients` (default `1000`)
- `-api-key` (overrides `PQHEAL_API_KEY` when nonempty; one credential source is required unless `-allow-unauthenticated` is set)
- `-allow-unauthenticated` (default `false`; allows insecure mode for local/testing only)
- `-key-store` (default `server-keys.json`; persisted hybrid encryption keyring path, empty disables persistence)
- `-evict-after` (default `0` disabled; removes clients not seen within this duration, e.g. `5m`)
- `-rate-limit-rps` (default `0` disabled; per-client token bucket refill rate in requests/sec)
- `-rate-limit-burst` (default `10`; per-client token bucket burst size, used with `-rate-limit-rps`)
- `-rate-limit-idle` (default `5m`; evicts per-client buckets idle longer than this)
- `-log-format` (default `text`; structured log format, `text` or `json`. In an interactive terminal, the latest three log records appear in a fixed dashboard area. Redirect stderr to a file to retain full logs. The dashboard updates in place, clips overflow to the terminal size, and restores the screen on exit; redirected stdout disables it.)
- `-access-log` (default `false`; logs one structured line per HTTP request — method, path, status, request_id, remote, duration — to stderr.)
- `-tls-cert` (default empty; path to the TLS certificate. Enables HTTPS; must be set together with `-tls-key`.)
- `-tls-key` (default empty; path to the TLS private key. Enables HTTPS; must be set together with `-tls-cert`.)
- `-model` (default empty; path to the trained anomaly detection model. When absent, unreadable or structurally invalid the server logs the degradation and runs without the Isolation Forest layer.)
- `-detect-queue` (default `1024`; readings buffered ahead of the detection goroutine. Submissions that would exceed it are dropped and counted rather than delaying ingest.)
- `-healthcheck` (probes the local `/health` endpoint and exits `0` when healthy. Lets external monitoring establish liveness by running the same binary, without needing an HTTP client on the node.)

### Anomaly detection

The server scores accepted telemetry with the layered model described in *D2.2
Anomaly Detection Model Report*. Detection runs off the request path: readings
are queued after they are stored and logged, so a detector fault cannot cost
telemetry and detection cost does not enter the client's response time.

Related sensors can supply the same optional `subject_id` on every `/ingest`
payload (CLI: `-subject-id patient-1`) while retaining distinct sensor client IDs.
For example, a motion client `motion-1` and a heart-rate client `heart-1` can both
use `patient-1`. Heartbeats still identify only the sensor. Baselines remain
per sensor; activity context is shared by subject. Without `subject_id`, the
sensor's own client ID is used, preserving independent streams.

The latest received motion reading by sensor timestamp supplies the subject's
activity context. If timestamps tie, the lexicographically smaller motion client
ID wins. Motion context is removed when its sensor is evicted; without a motion
source, the detector uses the resting regime. Changing a sensor's `subject_id`
resets its detection baseline. Subject associations are supplied with each
reading and are not restored from the historical CSV log.

Alerts are published as structured events carrying the client id, sensor type,
observed value, breached threshold or last-seen timestamp, the detection layer
and reason, and the originating request id. Each alert is classified
`emergency` or `routine`, which is the hub-side half of the differentiated
handling that binds to a testbed network slice at deployment.

```bash
./pq-heal-server -api-key your-secret -model anomaly-model.json
```

Without `-model` the server still applies the static thresholds, the
stream-integrity rules and the personalised adaptive bounds; only the Isolation
Forest layer is disabled.

### Deployment

The hub deploys as a native statically linked executable. There is no container
runtime, no interpreter and no shared-library dependency: one binary per
architecture, copied to the edge node and supervised by systemd.

```bash
./deploy/build-release.sh          # writes dist/pq-heal-server-linux-{amd64,arm64} + checksums
```

`deploy/pq-heal-hub.service` is the reference unit. It runs the hub under a
dedicated unprivileged account. The executable reads `PQHEAL_API_KEY` directly
from the environment file; systemd does not expand it into process arguments.
A nonempty `-api-key` overrides the environment value for compatibility; use the
environment file in deployment to keep the secret out of the command line.
The unit applies CPU and memory limits through
cgroup v2 so the hub coexists with other workloads on a shared edge node.
Installation steps are in the header of that file.

### Transport security (the API key is sent in cleartext without TLS)

The message-level hybrid encryption (`-encrypt`) protects the **payload body** of `/ingest` and `/heartbeat`,
but it does **not** protect the `Authorization: Bearer <api-key>` header. That header is sent on every request
— including encrypted ones — so on plaintext HTTP an on-path observer can read the API key and then forge auth
or call the operator endpoints (`/keys/rotate`, `/keys/all`, `/clients`).

**Whenever `-api-key` is set, run with TLS.** Either terminate TLS at the edge (reverse proxy) or enable the
server's built-in TLS:

```bash
./pq-heal-server -api-key your-secret -tls-cert server.crt -tls-key server.key
```

The server logs a warning at startup if `-api-key` is set without `-tls-cert`/`-tls-key`, on the assumption that
no edge terminator is in front of it.

### Abuse resistance and limits

The application-level rate limiter (`-rate-limit-rps`) is keyed by payload `id` and is applied *after*
a request is decrypted and validated, so it shapes per-client traffic but does not protect the hybrid
decryption path from a pre-auth flood of unique senders. Enforce volumetric/connection limits at the
edge (reverse proxy / TLS terminator) for that. As a backstop, the server caps its internal state so a
flood cannot exhaust memory: the client store honors `-max-clients` (with LRU eviction), and the
replay cache and rate-limiter bucket maps have hard size ceilings. At its ceiling the rate limiter
evicts the least recently used bucket, so a key-rotation flood cannot lock out new legitimate
clients; the replay cache instead fails closed (rejecting as a potential replay), since admitting an
entry it cannot remember would defeat its purpose. Periodic sweepers drain expired entries from both.
Capacity and idle eviction also release detector baselines and motion context.
Queued readings from an evicted registration are discarded and counted in the
`dropped` detection statistic, including when the same client ID registers again.
Eviction signals coalesce instead of accumulating an unbounded cleanup queue.

Replay protection applies to both encrypted and plaintext payloads. An encrypted envelope is keyed by
its random nonce; a plaintext body, which has none, is keyed by endpoint, `id`, `type` and the
client's own `timestamp`. Resending a captured body within the timestamp window (`MaxReplayAge` /
`MaxFutureSkew`) is therefore rejected with `replay_detected` in both modes. One consequence for
plaintext senders: a retry that reuses the original timestamp is treated as a replay, so a retrying
client must re-stamp the payload. This is not a substitute for encryption — a plaintext body is still
readable and modifiable in transit — so run with encryption and an API key in production.

## Run the client

```bash
go run ./cmd/client -api-key your-secret
```

Flags:

- `-server` (default `http://localhost:9000`)
- `-heartbeat` (default `5s`)
- `-timeout` (default `5s`)
- `-api-key` (default empty; must match server `-api-key` when auth is enabled)
- `-subject-id` (default empty; shared identity linking related sensors for activity-aware detection; each sensor still needs a unique client ID)
- `-encrypt` (default `false`; wraps `/ingest` and `/heartbeat` payloads in an encrypted envelope)
- `-key-pin` (required with `-encrypt`; expected SHA-256 fingerprint of the server `/keys` bundle)
- `-key-ttl` (default `1h`; refreshes the cached `/keys` bundle after this duration. The client also refetches eagerly when the server replies with `unknown_key_id` or `decryption_failed`, retrying the request once with the new bundle.)

The client will prompt for:

1. Client ID (unique)
2. Data type
3. Data transmission frequency

While running:

- Values are auto-generated each send based on the sensor type
- Each successful send prints `[sent] <type>=<value> <unit>` to stdout; errors go to stderr
- Type `quit` or press Ctrl+C to stop

## Server API

### `POST /ingest`

Accepts data payloads:

```json
{
  "id": "client-1",
  "type": "heart_rate",
  "value": 72.5,
  "unit": "BPM",
  "timestamp": "2026-01-31T18:40:00Z"
}
```

Validation errors return JSON:

```json
{
  "error": {
    "code": "timestamp_required",
    "message": "timestamp is required",
    "request_id": "f0c25ea4b1f28d3f3f96b90b"
  }
}
```

`X-Request-ID` is returned on all responses. If the request includes an `X-Request-ID` header, the same value is echoed back.
Successful `POST` responses include `request_id` in JSON (`{"status":"accepted","request_id":"..."}` and `{"status":"alive","request_id":"..."}`).

Possible codes:

- `method_not_allowed` (`405`)
- `unauthorized` (`401`)
- `rate_limited` (`429`)
- `client_type_mismatch` (`409`)
- `invalid_json` (`400`)
- `trailing_json_content` (`400`)
- `id_required` (`400`)
- `unsupported_type` (`400`)
- `invalid_status` (`400`)
- `value_required` (`400`)
- `value_not_number` (`400`)
- `motion_value_invalid` (`400`)
- `timestamp_required` (`400`)
- `timestamp_too_old` (`400`)
- `timestamp_in_future` (`400`)

Supported `type` values:

- `heart_rate`
- `person_temperature`
- `ambient_temperature`
- `motion`

`motion` accepts boolean or numeric values. All sensor types reject a missing or
`null` value with `value_required`; numeric zero and boolean false remain valid.

Units are fixed per type:

- `heart_rate` -> `BPM`
- `person_temperature` -> `C`
- `ambient_temperature` -> `C`
- `motion` -> `Movement`

### `POST /heartbeat`

Sends an alive signal:

```json
{
  "id": "client-1",
  "type": "heart_rate",
  "status": "alive",
  "timestamp": "2026-01-31T18:40:00Z"
}
```

`timestamp` is required and must be recent (no more than 2 minutes in the past, and no more than 15 seconds in the future).
Possible validation error codes are listed above.

### `GET /keys`

Returns the current server public key bundle used to encrypt message envelopes:

```json
{
  "key_id": "v1",
  "x25519_public": "base64...",
  "kem_public": "base64...",
  "kem_algorithm": "ML-KEM-768"
}
```

Use `-encrypt` on the client to fetch these keys and send encrypted envelopes to `/ingest` and `/heartbeat`.
Encrypted mode requires key pinning with `-key-pin` so the client can authenticate the `/keys` response.

Fingerprint format used by `-key-pin`:

`sha256( key_id + "|" + kem_algorithm + "|" + x25519_public + "|" + kem_public )` in lowercase hex.

New encrypted-payload validation codes:

- `unknown_key_id` (`400`)
- `invalid_encrypted_envelope` (`400`)
- `decryption_failed` (`400`)
- `replay_detected` (`400`)
- `unsupported_kem_algorithm` (`400`)

### `GET /keys/all`

Returns the active key set (current + previous keys still accepted for decryption). This is an
operator/rotation-visibility view and requires `Authorization: Bearer <api-key>` when auth is enabled
— clients only need the current bundle from `GET /keys`:

```json
{
  "current_key_id": "v2",
  "keys": [
    {
      "key_id": "v2",
      "x25519_public": "base64...",
      "kem_public": "base64...",
      "kem_algorithm": "ML-KEM-768"
    },
    {
      "key_id": "v1",
      "x25519_public": "base64...",
      "kem_public": "base64...",
      "kem_algorithm": "ML-KEM-768"
    }
  ]
}
```

### `POST /keys/rotate`

Rotates the current server key (requires `Authorization: Bearer <api-key>` when auth is enabled).
Recently retired keys remain active for decryption so in-flight requests survive rotation, and new
clients should use the new current key from `GET /keys`. The keyring is bounded: only the current key
plus the two most recent previous keys are retained — older keys are pruned (on rotation and on load)
so a long-retired key cannot decrypt fresh traffic and the keyring cannot grow without bound.
The response includes the new bundle plus its `fingerprint` (the SHA-256 used by `-key-pin`):

```json
{
  "key_id": "v2",
  "x25519_public": "base64...",
  "kem_public": "base64...",
  "kem_algorithm": "ML-KEM-768",
  "fingerprint": "<lowercase hex>"
}
```

### `GET /clients`

Returns a JSON array of all currently tracked clients (requires auth if `-api-key` is set):

```json
[
  {
    "id": "client-1",
    "type": "heart_rate",
    "value": "72.00",
    "unit": "BPM",
    "data_at": "2026-01-31T18:40:00Z",
    "last_seen": "2026-01-31T18:40:05Z",
    "has_value": true
  }
]
```

Clients that have only sent heartbeats (no data yet) will have `"has_value": false` and omit `value`, `unit`, and `data_at`.

### `GET /health`

Returns `ok`.


## Dashboard

### Web dashboard

Open `http://SERVER_HOST:9000/dashboard` (or `https://SERVER_HOST:9000/dashboard`
when TLS is enabled) for a responsive, read-only view of connected sensors.
The page shows the latest values from `GET /clients`, refreshes every five
seconds, and highlights clients not seen in the last ten seconds or readings
outside the configured heart-rate and person-temperature thresholds. Search and
status filters work locally in the browser. It does not display historical
alerts or replace a clinical record.

When the server uses an API key, the page prompts for it. The key is held only
in the current browser tab and sent to the same-origin `/clients` endpoint;
use HTTPS when accessing the dashboard from another machine. The page and its
styles/scripts are embedded in the server binary, so no separate web service
or network asset download is needed. Avenir is used when installed on the
viewing device, with a system sans-serif fallback.

The illustrative screenshots in `docs/` use locally generated sample readings.

### Terminal dashboard

```text
PQ Heal Server Dashboard
Server: http://localhost:9000
Listening on :9000 | Clients: 3 | Up: 4m | 19:10:02
────────────────────────────────────────────────────────────────────────────────

== HEART_RATE ==
CLIENT                  VALUE UNIT     DATA AGE   SEEN AGE   STATUS
--------------------------------------------------------------------------------
client-a                72.00 BPM      1s         0s         alive
SUMMARY: count=1 alive=100% avg=72.00

== PERSON_TEMPERATURE ==
CLIENT                  VALUE UNIT     DATA AGE   SEEN AGE   STATUS
--------------------------------------------------------------------------------
client-b                36.70 C        4s         0s         alive
SUMMARY: count=1 alive=100% avg=36.70

== MOTION ==
CLIENT                  VALUE UNIT     DATA AGE   SEEN AGE   STATUS
--------------------------------------------------------------------------------
client-c               motion Movement 7s         22s        stale
SUMMARY: count=1 alive=0% avg=-

Keys: Ctrl+C = quit
```

## Testing

If Go cache permissions are restricted, run tests with a temp cache:

```bash
GOCACHE=/tmp/go-build go test ./...
```

## Ubuntu 22.04 ARM64 handoff package

Run `./deploy/build-ubuntu22-arm64.sh` to build static server/client binaries, KPI tools, a trained model, scripts, source and checksums under `dist/`. See [the deployment and experiment guide](deploy/ubuntu22-arm64/GUIDE.md) for separate-machine HTTPS setup, systemd installation and the ten test experiments. The dedicated `scripts/run-kpis.sh` is included in the generated package.

The client reads `PQHEAL_API_KEY` when no explicit key flag is supplied. Client and KPI tools accept a private CA through `SSL_CERT_FILE` while retaining certificate and hostname verification. No network alert receiver or slice integration is included; full network alert latency and GDPR compliance are not automatically certified.

### Client source port CSV

The server writes each accepted `/ingest` and `/heartbeat` request to `client-ports.csv` (mode 0600 when created). Use `-client-port-log=/path/file.csv` to change the location or `-client-port-log=` to disable it. Columns: `timestamp,client_id,endpoint,client_ip,client_port,request_id`. The timestamp is server observation time; the address is the TCP peer seen by the hub, including any proxy/NAT translation, and may change on reconnect. Forwarding headers are not trusted. Missing/malformed peer addresses yield empty IP/port fields. Rejected requests are not recorded. Existing telemetry CSV files are unchanged.

The Ubuntu systemd deployment stores it at `/var/lib/pq-heal/client-ports.csv`. Include its retention/access policy with the other logs.

## Ubuntu 22.04 AMD64 (Intel/AMD x64) handoff package

Run `./deploy/build-ubuntu22-amd64.sh` to build the complete native Linux x86-64 package at `dist/pq-heal-ubuntu22.04-amd64.tar.gz`, with its `.sha256` checksum. Send both files to the deployment team. See [the AMD64 deployment guide](deploy/ubuntu22-amd64/GUIDE.md). The target machines must report `x86_64` from `uname -m`. This package includes the server, client, KPI and benchmark binaries; it does not require ARM emulation or a Go installation.

## Funding and disclaimer

![Co-funded by the European Union](docs/funding/eu-co-funded.png)
![6G Smart Networks and Services](docs/funding/6g-sns.png)
![6G-PATH](docs/funding/6g-path.png)

PQ-HEAL has indirectly received funding from the Smart Networks and Services Joint Undertaking (SNS JU) under the European Union's Horizon Europe research and innovation programme, via the 6G-PATH – Open Call #2 issued and executed under the 6G-PATH project (Grant Agreement no. 101139172).

This publication reflects only the authors’ views. The European Commission, the SNS JU and the 6G-PATH consortium are not liable for any use that may be made of the information it contains.

## License

PQ-HEAL is licensed under [Creative Commons Attribution 4.0 International (CC BY 4.0)](LICENSE).

The funding emblems and logos are subject to their respective usage rules and are excluded from the repository’s CC BY 4.0 license.
