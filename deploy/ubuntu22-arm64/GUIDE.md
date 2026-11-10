# PQ-HEAL: quick deployment and tests

**Two Ubuntu 22.04 ARM64/aarch64 machines:** one server, one client. Use synthetic data. No Go installation is needed.

## 1. On both machines

Install missing tools, verify the downloaded archive, and extract it:

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl openssl python3
sha256sum -c pq-heal-ubuntu22.04-arm64.tar.gz.sha256
tar -xzf pq-heal-ubuntu22.04-arm64.tar.gz
cd pq-heal-ubuntu22.04-arm64
```

Keep clocks synchronized (`timedatectl status`). Allow the client to reach the server's TCP port **9000**.

## 2. On the server — one command

Replace `192.0.2.10` with the server's actual IP:

```bash
sudo ./scripts/deploy-server.sh 192.0.2.10
```

This creates test TLS certificates and an API key, installs/starts the systemd service, obtains the trusted key fingerprint, and creates **`client-setup.tar.gz`**. No manual configuration or pin copying is needed.

Transfer `client-setup.tar.gz` privately to the client machine. It contains its API credential and public CA certificate, **no private keys**. The test server certificate lasts 30 days. For site certificates or an existing installation, see [REFERENCE.md](REFERENCE.md).

## 3. On the client — extract and run

Place `client-setup.tar.gz` inside the extracted package directory:

```bash
tar -xzf client-setup.tar.gz
./scripts/run-kpis.sh verify
./scripts/run-client.sh
```

Verification should finish with **exit 0**. The client then sends encrypted heart-rate readings every five seconds. Stop it with **Ctrl+C** before performance tests.

To start **10 clients** together (replace `10` with the desired count):

```bash
./scripts/run-clients.sh -10
# Equivalent: ./scripts/run-clients.sh -n 10
```

Ctrl+C stops the whole group. Each client gets a unique client/subject ID and its own log under `results/clients-.../`; `clients.tsv` maps IDs to processes/logs. They use the sensor type and frequency from `config/client.env`. Limit: 1–1,000 per run; the hub's 1,000-client capacity is shared across all runs and machines. Avoid exceeding it. For a custom configuration: `./scripts/run-clients.sh -10 config/second-client.env`.

To run another sensor, copy `config/client.env`, change `PQHEAL_CLIENT_ID` and `PQHEAL_SENSOR_TYPE`, then run `./scripts/run-client.sh config/second-client.env`. Types: `heart_rate`, `person_temperature`, `ambient_temperature`, `motion`. Related sensors should share `PQHEAL_SUBJECT_ID`.

## 4. Run the experiments

Start the server first. Run one experiment at a time unless measuring load. Repeat performance tests **three times**, retaining all runs.

| Experiment | Machine and command | Check |
|---|---|---|
| Encrypted/security checks | Client: `./scripts/run-kpis.sh verify` | All checks pass; match the threshold-injection request ID with a server alert log. |
| Telemetry/liveness | Client: `./scripts/run-client.sh`; stop after two minutes | Server logs a liveness-loss alert after about 10–15 seconds. |
| Network timing/PQC overhead | Client: `./scripts/run-kpis.sh remote` | About 100 seconds; inspect latency, overhead and failed requests. |
| Local alert timing | Server: `./scripts/run-kpis.sh local` | No missing alerts; inspect local alert latency. |
| Crypto primitives | Either: `./scripts/run-kpis.sh crypto` | Encryption/decryption timings, allocations and wire sizes. |
| Detection | Server: `./scripts/run-kpis.sh detection` | Event recall ≥95%; also report false positives and reading recall. |
| Permissions | Server: `sudo ./scripts/run-kpis.sh privacy` | Technical checks pass; manual privacy review remains required. |
| Restart recovery | Server: `sudo ./scripts/run-kpis.sh recovery` | Run verification first; expect preserved keyring and persisted clients. Deliberately restarts the service. |
| CPU/memory | Server: `DURATION_SECONDS=120 ./scripts/run-kpis.sh host` | Run once idle and once while clients send data; compare CSV samples. |

Optional coordinated key-rotation and fault-injection steps are in [REFERENCE.md](REFERENCE.md).

**Read results correctly:**

- Full network alert latency (target ≤10 ms) is **not measured**: no network receiver or slice integration is included. Local alert timing and HTTP round-trip timing are different measurements.
- PQC overhead (target ≤15%) compares hybrid-envelope requests with ordinary JSON over the same HTTPS connection: `100 × (hybrid mean / baseline mean − 1)`.
- Detection results use the supplied **synthetic corpus**, not clinical validation. GDPR compliance requires manual review.

The server also records accepted clients’ source IPs/ports in `/var/lib/pq-heal/client-ports.csv` (one row per telemetry/heartbeat request; proxy/NAT addresses are those observed by the server).

## 5. View status and send results

On the server:

```bash
sudo systemctl status pq-heal-hub --no-pager
sudo journalctl -u pq-heal-hub -n 50 --no-pager -o cat
# Restart only when needed:
sudo systemctl restart pq-heal-hub
```

Each experiment saves a timestamped folder under **`results/`**, including raw measurements, machine details and `EXIT_STATUS`. Nonzero status means failed/incomplete execution; inspect target comparisons separately.

On each machine, send its results with the machine name and test conditions:

```bash
tar -czf "results-$(hostname).tar.gz" results
```

Do not include `config/`, `state/` or `client-setup.tar.gz` in the results handoff. For TLS/pin errors, check the server IP, clock and certificates; never disable verification. Detailed measurement definitions, troubleshooting, upgrades and source-build instructions are in [REFERENCE.md](REFERENCE.md).
