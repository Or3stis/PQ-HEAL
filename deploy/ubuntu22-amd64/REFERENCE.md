# PQ-HEAL deployment and experiment guide

**Target:** Ubuntu Server 22.04 LTS, AMD64 (`x86_64`). **Topology:** separate server and client machines. **Data:** synthetic test data only. **Alert handling:** existing server logs and internal alert bus; no network receiver is included.

## 1. What to send and what this package establishes

Send `pq-heal-ubuntu22.04-amd64.tar.gz` and its `.sha256` file to the colleague through a trusted channel. This archive contains five static AMD64 executables, a trained anomaly model, compressed training/evaluation corpora, Bash launch/install/KPI scripts, this guide, checksums and a source archive. Go is not needed to run the binaries. No API credential, private key, existing patient telemetry or server keyring is shipped.

| Executable | Purpose |
|---|---|
| `bin/pq-heal-server` | Deployed Home Hub, HTTPS, detection, CSV persistence and terminal dashboard |
| `bin/pq-heal-client` | Simulated sensor client using a pinned hybrid X25519 + ML-KEM-768 envelope |
| `bin/pq-heal-kpibench` | Remote verification, timed remote requests, local integrated measurements and fingerprint retrieval |
| `bin/pq-heal-detection-bench` | Evaluate the shipped model or reproducibly train a new one on the supplied corpus |
| `bin/pq-heal-crypto-bench` | Compiled Go benchmark runner for encryption, decryption, round-trip, key generation and wire sizes |

`BUILD-INFO.txt` identifies the build toolchain, base revision and working-tree changes. `source.tar.gz` contains the actual source used, including uncommitted work. `model/build-host-evaluation.*` contains build-host results, **not Ubuntu AMD64 testbed measurements**. Use the KPI runner on the target machines to produce fresh evidence.

### KPI scope and source mapping

| Requirement | Experiment / evidence | Interpretation |
|---|---|---|
| KPI 1: alert latency at most 10 ms | E4 local request-to-alert-publication timing; E3 separate-machine request/response timing | Partial evidence only. Neither measures receipt of a network alert. Full KPI 1 and slice validation remain **NOT MEASURED**. |
| KPI 2: overhead at most 15% | E3 paired hybrid-envelope versus JSON-without-envelope over identical HTTPS transport; E5 primitives | Exact baseline/formula is declared below. This is not an X25519-only encrypted-envelope comparison. Report measured overhead even if above target; do not change the denominator to manufacture a pass. |
| KPI 3: full GDPR compliance | E7 technical controls plus manual assessment | Scripts cannot establish legal compliance. Record **MANUAL REVIEW REQUIRED** until project governance completes its assessment. |
| KPI 4: at least 95% event detection | E6 held-out labelled synthetic corpus, shipped model | Event-level recall is the report's target metric. Report reading recall, FPR, precision and accuracy too. Synthetic success does not establish clinical or real-testbed accuracy. |
| Recovery and shared-node operation | E8 recovery, E9 resource sampling | Short-run engineering evidence, not proof of 99.999% system availability. |

Source traceability: D3.1 Testbed Deployment Report v1.0, sections 4-6 (packaging, acceptance and KPI targets); D2.3 Prototype Home Hub Report v1.0, sections 5-10 (alerts, observability, resource limits, local performance); D2.2 Anomaly Detection Model Report v1.0, sections 5-7 (corpus, event metrics, inference); D2.2 Protocol Implementations Report v1.0 (handler/envelope path); D2.1 Prototype PQC Libraries v1.0 and its supplied `d21_bench_test.go` (primitive benchmarks). Corpora are copied unchanged from `D2.2b_supporting/*.csv.gz` and their checksums are included.

The documents describe intended deployment capabilities beyond this checkout: there is **no external alert forwarding, slice/DSCP binding or browser dashboard**. All API endpoints share the same listener; choosing a bind IP does not by itself put `/health`, `/keys` and management APIs on different planes. Site firewall/reverse-proxy rules must provide path-specific exposure if required. Remote operators can consume authenticated `/clients` JSON. Do not mark the full D3.1 acceptance matrix as passed on this package alone.

## 2. Prepare both machines

1. Confirm `uname -m` prints `x86_64` and `/etc/os-release` identifies Ubuntu 22.04. These launch scripts deliberately reject other platforms.
2. Record server/client hardware, RAM, kernel, network interface, wired/RAN path, MTU, RTT, test date and concurrent workloads. Use the same machines and network path for comparisons.
3. Ensure time synchronization is active on both nodes: `timedatectl status`. Envelopes older than two minutes or more than 15 seconds in the future are rejected. Same-process timing uses a monotonic clock; request timestamps still require synchronized wall clocks.
4. Confirm server data-plane IP/DNS and TCP port 9000 are reachable from the client. Permit the client source at the site's firewall; restrict management access according to site policy. Do not expose the test service publicly.
5. Ensure standard tools exist: Bash, `tar`, `gzip`, `sha256sum`, `curl`, OpenSSL, Python 3, `lscpu`, and systemd (server service only). If absent, the node operator can install `ca-certificates curl openssl python3` using Ubuntu's package manager. The Go executables need no runtime installation.
6. Verify transfer integrity and extract, on **both** machines:

```bash
sha256sum -c pq-heal-ubuntu22.04-amd64.tar.gz.sha256
tar -xzf pq-heal-ubuntu22.04-amd64.tar.gz
cd pq-heal-ubuntu22.04-amd64
sha256sum -c SHA256SUMS
```

Use the archive checksum received through the trusted handoff channel. A checksum accompanying an untrusted download does not establish authenticity.

## 3. Configure HTTPS and credentials

Choose **one** setup. All commands below run from the extracted package root. The configuration files are trusted Bash files, sourced by the scripts: never source configuration supplied by an untrusted party.

### A. Isolated lab setup

On the server, replace the example address with the actual server data-plane IP:

```bash
./scripts/setup-lab.sh 192.0.2.10
# With DNS instead: ./scripts/setup-lab.sh hub.example.test 192.0.2.10
```

This creates a private test CA, a server certificate with the correct IP/DNS SAN, a random API key, and `config/server.env` / `config/client.env`. The CA expires in 90 days and the server certificate in 30 days. This is a lab identity, not site PKI. Keep all private keys on the server; the CA private key is not required by either running process.

On the server, start a foreground instance for initial setup:

```bash
mkdir -p results
chmod 700 results
./scripts/run-server.sh config/server.env 2>results/server.jsonl
```

In a second server terminal, obtain the **message-key fingerprint over the trusted TLS connection**:

```bash
SSL_CERT_FILE="$PWD/config/lab-ca.crt" \
  ./bin/pq-heal-kpibench -mode=pin -server=https://192.0.2.10:9000
```

Set `PQHEAL_KEY_PIN` in `config/client.env` to that 64-character output. Send only `client.env`, `lab-ca.crt`, and the pin through a trusted channel to the client machine. **Do not send `server.env`, `lab-ca.key`, `lab-server.key` or `state/server-keys.json`.** On the client, edit `SSL_CERT_FILE` to the absolute local path to the received CA file. Then:

```bash
chmod 600 config/client.env
./scripts/run-client.sh config/client.env
```

Never obtain a pin using disabled TLS verification. `-mode=pin` relies on verified TLS and has no `--insecure` option. Keep the same server state/keyring when moving from foreground to systemd; see section 4 before switching.

### B. Site-issued certificate

Copy `config/server.env.example` to `config/server.env`, set a random API key (for example, generated by `openssl rand -hex 32`), the bind IP, and the absolute paths to a certificate/full chain and private key. The certificate must match the hostname or IP in `PQHEAL_SERVER_URL`. Set file mode 600. On the client, copy/edit `client.env.example`, configure the same API credential and the trusted message-key fingerprint, and set `SSL_CERT_FILE` only if a private CA is needed. Use the OS trust store otherwise.

Do not pass credentials using `-api-key`, paste them into shell history, or enable shell tracing. The scripts use `PQHEAL_API_KEY` from configuration/environment. TLS protects the Bearer header; the hybrid envelope protects each telemetry payload.

## 4. Deploy the server under systemd

Foreground and systemd modes are alternatives; never run both on the same address/port.

The fresh installer uses `/opt/pq-heal`, `/etc/pq-heal` and `/var/lib/pq-heal`, creates an unprivileged `pqheal` account, checks all bundle checksums and enables the service. It refuses an existing installation. cgroup v2 must be available (`/sys/fs/cgroup/cgroup.controllers`).

For a fresh service with no previous foreground state:

```bash
sudo ./scripts/install-server.sh "$PWD/config/server.env"
sudo systemctl status pq-heal-hub --no-pager
sudo journalctl -u pq-heal-hub -n 30 --no-pager -o cat
```

If you already started the foreground server in section 3, stop it with Ctrl+C first. To preserve its message-key pin and registry, create the service account/directories and transfer state **before** installation:

```bash
getent group pqheal || sudo groupadd --system pqheal
id pqheal || sudo useradd --system --gid pqheal --no-create-home --shell /usr/sbin/nologin pqheal
sudo install -d -o pqheal -g pqheal -m 0750 /var/lib/pq-heal
sudo install -o pqheal -g pqheal -m 0600 state/server-keys.json /var/lib/pq-heal/
# Only if the foreground run created telemetry:
sudo install -o pqheal -g pqheal -m 0600 state/data.csv /var/lib/pq-heal/
sudo ./scripts/install-server.sh "$PWD/config/server.env"
```

If you intentionally start with a new state directory instead, the keyring and fingerprint change: retrieve and distribute the new pin before starting clients. Do not silently reuse the old pin.

Confirm a log entry `anomaly model loaded`, with all three quantitative sensor forests, and no model-degradation warning. The server deliberately falls back to L1-L3 if loading fails; a healthy HTTP endpoint alone is therefore insufficient evidence for KPI 4 readiness.

Service limits: CPUQuota 50% of **one core**, MemoryHigh 192 MiB, MemoryMax 256 MiB, TasksMax 64. Record these with results. The state directory is writable by the service; code/model are read-only. Telemetry/keyring files are mode 600; root-owned service configuration and TLS key are mode 640 with group `pqheal` so the service can read them. This group-readable configuration is an explicit packaging choice relative to D3.1's mode-600 environment-file example.

The server binds only the configured IP, so its built-in loopback `-healthcheck` may not work for that binding. Use `scripts/api.sh config/client.env GET /health` with the actual HTTPS URL instead.

## 5. Experiments and result collection

Run experiments with synthetic identities, without concurrent unrelated tests unless load is the intended variable. Each KPI run creates a private, timestamped `results/<experiment>-.../` directory containing environment/build metadata, logs, measurements and `EXIT_STATUS`. Nonzero status means the experiment failed or evidence is incomplete; inspect it before interpreting numbers. Target misses are recorded in JSON and do not necessarily produce a nonzero process exit.

Keep three independent runs per performance experiment. Report all runs, sample counts, failures and variation, not just the fastest run. Avoid combining samples across different hardware, service limits or network configurations. `RESULTS_DIR` may select another writable destination. Never share private keys or environment files with the result archive.

### E1. Functional and cryptographic acceptance (client machine)

Server must already be running with the shipped model and client configuration complete:

```bash
./scripts/run-kpis.sh verify config/client.env
```

Checks: HTTPS/key pin, liveness, unauthenticated denial/authenticated registry, encrypted ingest for all four sensor types, heartbeat, replay, tampering, stale timestamp and null-value rejection. The final injected heart rate is 165 BPM; locate its `request_id` in `verification.json` and the server's `alert` log. Expect `priority=emergency`, `kind=threshold_breach`, and layer information. Acceptance of the request alone does not prove alert publication; retain that matching log entry.

On a systemd server, export logs for the actual test window:

```bash
sudo journalctl -u pq-heal-hub --since '2026-09-18 12:00:00 UTC' \
  --until '2026-09-18 12:10:00 UTC' -o cat > results/e1-server.jsonl
```

Replace the timestamps with your run's UTC times. Failed verification does not stop the service. It leaves synthetic registry entries and telemetry for audit/recovery tests.

### E2. Separate-machine steady-state telemetry and liveness

On the client:

```bash
./scripts/run-client.sh config/client.env
```

Keep it running for at least two minutes. Confirm initial and recurring successful ingest/heartbeat, then inspect the server registry:

```bash
./scripts/api.sh config/client.env GET /clients
```

To test all streams, make four private copies of `client.env`, using unique `PQHEAL_CLIENT_ID` values and types `heart_rate`, `person_temperature`, `ambient_temperature`, `motion`. Give related sensors the same `PQHEAL_SUBJECT_ID`. Launch one script per configuration in separate terminals. Each sends every five seconds plus five-second heartbeats. The client is a simulator, not a real sensor driver.

Stop a heart-rate client with Ctrl+C and wait at least 15 seconds. Expect a `liveness_loss` emergency log. Stop an ambient/motion client and expect a routine liveness log. This exercises both priorities **locally**; it does not exercise network slices or fall detection. Start the same client again and confirm liveness recovers. Stop all simulation clients before isolated latency measurements.

### E3. Separate-machine encrypted transport and overhead (client machine)

```bash
SAMPLES=1000 INTERVAL=50ms ./scripts/run-kpis.sh remote config/client.env
```

Repeat three times. Default duration is approximately 100 seconds plus processing/network time: 1,000 pairs, two requests per pair, 50 ms pause after each request. It rotates across all four sensor types and alternates which mode is sent first. Both modes use the same HTTPS server, credential and transport; one sends ordinary JSON and one wraps it in the shipped hybrid envelope. Only synthetic telemetry is used for the baseline. Deployed clients still always use `-encrypt`; the server does not currently enforce encrypted-only payloads.

`data/samples.csv` records per-request preparation, HTTP, total elapsed time, status/error code and request ID. `data/measurements.json` reports mean/p50/p95/p99/max and failures. Units are milliseconds with sub-millisecond precision. Percentiles use the sorted element at floor((n-1)*p), only for successful requests; failures remain explicit and invalidate an unconditional success claim.

**Formula:** overhead (%) = 100 × (mean hybrid total / mean baseline total − 1). Total begins before payload JSON/envelope creation and ends after response-body read. The separate HTTP distribution excludes client encryption/preparation. Bootstrap, key retrieval and initial TLS setup precede timing; ten encrypted warm-up requests precede the workload. This measures a warm persistent connection, not first-connection latency. Negative overhead is possible from noise; report repeats.

The 15% comparison applies only to this declared **JSON-over-HTTPS baseline**. No classical-only message envelope exists in this checkout, so do not call this a pure ML-KEM-versus-X25519 comparison. If the project requires that baseline, mark it pending. Request/response latency is not alert latency. The output always leaves full KPI 1 unmeasured.

Default rate is intentionally low and suitable for shared nodes. `INTERVAL=0` produces an unpaced run, may hit rate limits and is not a standardized capacity result. Request caps are 20,000 pairs; the replay cache holds 100,000 entries. Separate high-rate runs by at least three minutes or isolate them on a fresh test service to avoid cumulative replay-cache capacity failures. Do not explain every rejection as replay-cache exhaustion: inspect its error code. Returned requests/s uses actual elapsed time and is **paced throughput**, not saturation capacity.

### E4. Local integrated request-to-alert timing (server machine)

This starts a temporary in-process HTTPS handler on loopback; it does not attach to or restart the installed service:

```bash
SAMPLES=1000 ./scripts/run-kpis.sh local
```

It loads the shipped model, enables temporary CSV telemetry logging, sends 165 BPM threshold breaches through the real handler/detection pipeline, and correlates an in-process timing sink by response request ID. Each mode reports `*_local_alert` timing from HTTP-request issue to internal alert publication, excluding client envelope preparation. Missing alerts after one second are failures, not zero-latency samples. The temporary telemetry is kept with the run.

This is a **local component measurement** related to D2.3 section 9, not a measurement of the deployed service's resource confinement or an operator receiving an alert. It uses a direct timing sink instead of production log/channel sinks. The L1 threshold path short-circuits deeper scoring even though the model is loaded; E6 measures the detector separately. Compare the distribution and maximum with 10 ms for context, but do not declare the full project KPI passed. No receiver has been added.

### E5. Cryptographic primitives (both machines, separately)

```bash
./scripts/run-kpis.sh crypto
```

Three repetitions of one-second Go benchmarks measure key generation, encryption, decryption, cryptographic round-trip and JSON-marshalling baseline, with ns/op, bytes/op and allocations/op. Size tests report plaintext/envelope/public-bundle bytes. These run offline with ephemeral keys. Report CPU and Go build version beside results. Primitive timing and wire expansion are not the same denominator as E3 end-to-end overhead; do not compare them directly to the 15% target.

### E6. Detection effectiveness and inference (server machine)

```bash
./scripts/run-kpis.sh detection
```

The runner decompresses the original corpora into the run directory and **evaluates the shipped model without retraining it**. `detection.json` contains event hits/total, event recall, reading recall, false-positive rate, precision, accuracy and TP/FP/TN/FN. `run.log` contains per-class figures, layer ablations and inference timing.

An event is a contiguous labelled run on one client/type; a label change, intervening normal reading or gap above 120 seconds splits it. Any detected constituent reading counts the event as hit. KPI 4 uses event recall ≥95%, as D2.2 section 6 specifies. Report results for each anomaly class too. FPR = FP/(FP+TN); precision = TP/(TP+FP). A passing result is scoped to this synthetic corpus and must not be presented as clinical validation or a new real-testbed dataset result.

Model fitting uses only the anomaly-free training split, fixed seed 20260720, and the current Go detector implementation. To explicitly retrain for development (never overwrite the deployed model during experiments):

```bash
mkdir -p results/retraining
# Replace RUN_DIR with the detection run directory that holds decompressed corpora.
./bin/pq-heal-detection-bench -train=RUN_DIR/ad_train.csv -eval=RUN_DIR/ad_eval.csv \
  -model-out=results/retraining/candidate-model.json \
  -json-out=results/retraining/candidate-evaluation.json \
  >results/retraining/candidate-evaluation.txt
```

Do not promote a candidate using held-out labels for tuning. Record candidate hashes and retain the previous model for rollback.

### E7. Privacy/security technical evidence (server machine)

After E1 has created state:

```bash
sudo ./scripts/run-kpis.sh privacy
```

The output records state/configuration permissions and selected service confinement. Inspect that neither command lines nor access logs disclose the API credential; access logs should contain paths, not query strings. Telemetry CSV and alert logs contain synthetic health-like data and identities; treat real replacements as sensitive.

Manual evidence to record separately: approved data purposes/legal basis, DPIA where applicable, consent/governance decisions, authorized recipients, retention and deletion periods for CSV/logs/backups/results, subject-rights procedures, incident process, access control, disk/backup protection, and institutional approval. This is an evidence checklist, not a legal compliance determination. The package does not implement retention deletion, encryption at rest or a clinical escalation process. KPI 3 remains manual review required.

### E8. Restart recovery and optional fault recovery (server machine)

Run E1 first so persisted clients exist. Pause other experiments. This command deliberately restarts the installed service:

```bash
sudo ./scripts/run-kpis.sh recovery "$PWD/config/client.env"
```

It retains before/after client snapshots, compares keyring SHA-256, measures restart-to-first-healthy-probe time and checks that clients with persisted readings reappear. Heartbeat-only clients are not persisted by the CSV history. The measurement includes `systemctl restart`, probe overhead and 100 ms polling resolution; this is not zero-downtime or five-nines evidence. In-flight requests can fail during a process restart. Keep telemetry evidence under the service's 1,000-client capacity and avoid eviction during this test.

Optional **fault injection**, only in the agreed test window: keep a client running, record `systemctl show pq-heal-hub -p NRestarts`, run `sudo systemctl kill --kill-whom=main --signal=SIGKILL pq-heal-hub`, and wait for `Restart=on-failure` (5-second delay). Check health, NRestarts, key continuity, registry and client errors. Save a journal export and record the observed outage. SIGKILL intentionally skips clean shutdown; do not use it on live patient monitoring.

### E9. Resource use under steady load (server machine)

Start E2's clients on the other machine, then:

```bash
DURATION_SECONDS=120 ./scripts/run-kpis.sh host
```

`resources.csv` samples interval CPU (100% = one whole core), resident memory in bytes and thread count once per second. `service.txt` records service caps. Run once idle and once with the recorded number of clients. Sampling fails if the process disappears or its PID is reused. Compare RSS with MemoryHigh/MemoryMax while remembering cgroup memory includes more than process RSS. Keep raw observations; a short mean below the cap is not proof of long-run stability.

### E10. Key rotation and management access (manual coordinated experiment)

Keep one encrypted client running; obtain a pre-rotation registry snapshot. On an authorized machine:

```bash
./scripts/api.sh config/client.env GET /keys/all > results/keys-before.json
./scripts/api.sh config/client.env POST /keys/rotate > results/rotation.json
./scripts/api.sh config/client.env GET /keys/all > results/keys-after.json
```

The rotation response includes the new `fingerprint`. Securely update all client configuration pins and restart clients to adopt it. Old cached bundles can decrypt because previous keys are retained, but a **fresh** client with the old pin will reject the new current bundle. Thus this package does not promise automatic pin rollout or indefinite interruption-free rotation. Record response codes and observed interruptions. Key-set responses contain public material; never export the private on-disk keyring.

Test authorized `/clients` access across the actual management path and unauthorized rejection. Site-level plane isolation must be tested from both networks against operator-defined routing/proxy rules; this script cannot infer that topology. Record browser dashboard, slice differentiation, external alert receipt and real-data evaluation as outstanding if absent.

## 6. Handoff results and troubleshooting

Provide the colleague's result directories, selected server journal windows and a short run sheet: operator, UTC interval, experiment number, client count/cadence, topology, machine details, clock state, service configuration (without secrets), artifact/model hashes, failures and outstanding checks. For example, from the package root:

```bash
tar -czf pq-heal-test-results.tar.gz results
sha256sum pq-heal-test-results.tar.gz > pq-heal-test-results.tar.gz.sha256
```

Review the selected contents before transfer. Do not include `config`, `state`, private keys, CA signing keys or uncontrolled real telemetry. Result directories can contain client snapshots and alert/telemetry samples, so follow the agreed retention/access rules.

| Symptom | Action |
|---|---|
| Exec format error | Confirm x86_64, not x86_64 or 32-bit ARM |
| TLS unknown authority / name mismatch | Fix CA path, full chain, URL/SAN, clocks or expiry; do not disable verification |
| Pin mismatch after install/restart | Check whether a new state directory generated a new keyring; obtain the trusted pin again |
| 401 | Verify private client/server API credentials match |
| timestamp too old / in future | Synchronize both clocks |
| 429 | Inspect rate-limit settings, lower test rate, and retain rejected counts |
| replay/capacity rejection | Inspect exact error code; wait for replay TTL before another flood test |
| health OK but model warning | Correct model path/permissions and restart; do not claim four-layer readiness |
| Service failed | Inspect journal, file access, bind address, port conflicts, cgroup v2 and memory limits |
| No graphical dashboard under systemd | Expected: the terminal dashboard runs only on a TTY; use authenticated `/clients` and journal |
| High overhead | Report it with its baseline, counts and variance; no target is guaranteed by the package |

## 7. Development build, upgrade and rollback

The runtime does not need Go. To develop/rebuild on an approved build host, use Go **1.26.3 or newer** as required by `go.mod`; Ubuntu 22.04's default Go package may be older. The source archive includes dependencies' version/hash requirements; a first build needs module download access or a prefilled module cache. From extracted `source.tar.gz`:

```bash
go test -race ./...
go vet ./...
# On AMD64, race testing requires a supported native compiler/toolchain.
OUT="$PWD/release-new" ./deploy/build-ubuntu22-amd64.sh
```

The build script cross-compiles static Linux AMD64 binaries and fits the model on the build host. It refuses an existing output directory, includes source and generates per-file/archive checksums. Build-host tests/cross-compilation do not certify Ubuntu service execution; complete E1-E9 on the actual target.

For an upgrade, verify the new bundle first, stop clients and service in a planned window, and make an access-controlled backup of `/opt/pq-heal`, `/etc/pq-heal` and `/var/lib/pq-heal`. Preserve the state directory and configuration. Replace `/opt/pq-heal/bin/pq-heal-server`, the model and launcher scripts using `sudo install` with their existing modes/ownership; review any unit changes, then `sudo systemctl daemon-reload` and `sudo systemctl start pq-heal-hub`. Repeat E1, model-load verification and pin continuity checks. The fresh installer deliberately does not perform upgrades automatically.

To roll back, stop the service and restore the previous binaries/model/configuration from the controlled backup, retaining the keyring/data unless recovery specifically requires the backup. Restart and repeat acceptance checks. Do not delete a state directory as a routine way to resolve a key mismatch.

## Multiple simulated clients on one client machine

Use `./scripts/run-clients.sh -10` (or `-n 10`) after importing `client-setup.tar.gz`. The launcher runs ten encrypted client processes, using the configured sensor type and interval, with distinct automatically generated client IDs and subject IDs. It never edits the shared configuration or puts the API credential on the command line. Multiple launches receive different ID prefixes; restarting a group creates new identities. Old entries remain until the hub evicts them (15 minutes in the packaged deployment). Count all groups and machines against the shared 1,000-client capacity.

Each run saves a private `results/clients-.../` folder with per-client logs and `clients.tsv`. Ctrl+C/TERM stops and reaps the group; an unexpected child exit stops the other clients and returns nonzero. Normal client transport errors are logged and retried by the existing client; a running process alone does not confirm successful delivery. IDs identify independent synthetic subjects; for related sensors sharing a subject, use the individual client launcher and explicit configurations.
