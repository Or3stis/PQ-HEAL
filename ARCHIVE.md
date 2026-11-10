# PQ-HEAL source archive

This snapshot contains the Go client, hub, anomaly detection and benchmark commands, the embedded web dashboard, and Ubuntu deployment scripts and documentation.

Run `go build ./...` and `go test ./...` from the archive root. See README.md for configuration and usage.

The evaluation corpora are distributed separately from this source snapshot. The Ubuntu deployment builders expect `ad_train.csv.gz` and `ad_eval.csv.gz` in the target architecture's `deploy/ubuntu22-*/assets/` directory. See the corresponding REFERENCE.md for their provenance.
