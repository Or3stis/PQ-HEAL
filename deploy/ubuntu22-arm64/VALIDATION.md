# Build validation record

Validated on the development host on 2026-09-18:

- Complete Go regression suite with race detector: passed (`go test -race ./...`).
- Static analysis: passed (`go vet ./...`).
- Bash syntax checks for every launcher, installer, KPI runner and builder: passed.
- Native HTTPS smoke test using a private CA: pin retrieval, encrypted verification for all sensor types, rejection of replay/tampering/stale timestamps/null readings, paired remote measurements and matching emergency alert log all passed.
- Native simulated client authenticated through the environment, sent encrypted telemetry and stopped cleanly.
- Native server restart preserved keyring bytes and recovered persisted client identities.
- Local integrated measurement returned correlated alerts with zero missing alerts in the smoke test.
- The supplied synthetic corpus reproduced 183/186 detected events (98.3871%), with TP=386, FP=110, TN=91798, FN=220. This is not target-hardware or clinical validation.
- Each release executable is an ELF AArch64 static binary, with no ELF interpreter requirement. Release checksums were verified.

Not executed on this host: Ubuntu 22.04 ARM64 binaries, privileged systemd installation, cgroup enforcement, Linux /proc resource sampling, site routing/plane isolation, the site's certificates, network slicing or real-data accuracy. The colleague must perform the guide's applicable experiments on both target machines. No network receiver was added and full network alert latency remains unmeasured.

The smoke-test server used temporary test credentials; those credentials and temporary private keys are not distributed. Per-run performance values from this host are not promoted to project KPI results.

Simplified handoff update (2026-09-19): Bash syntax validation passed for the new deploy/export launchers. An isolated shell fixture verified portable client paths, installed-credential precedence, repeat export, mode-600 handoff archives and an exact allowlist containing only client configuration and the public CA. The fixture mocked platform/privilege checks and key retrieval; it does not constitute a systemd/Ubuntu execution test.

Client port CSV update (2026-09-19): Added accepted-request logging for ingest and heartbeat, using the TCP peer rather than forwarding headers. Regression coverage checks IPv4/IPv6, missing peer data, request correlation, rejection/replay exclusion, append/reopen behavior, concurrent writes, mode 0600 and incompatible-header protection. Existing telemetry history keeps its original schema.

Multi-client launcher update (2026-09-28): An isolated host fixture verified `-3` and `-n 3`, invalid-count rejection, unique client/subject IDs, encrypted arguments, environment credentials, private per-client logs, Ctrl+C/TERM cleanup and group cleanup after a child failure. The fixture mocks platform/config checks and uses fake client processes; Ubuntu ARM64 execution still requires target-host verification. No Go runtime changes were needed.
