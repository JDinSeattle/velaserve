# Frozen Z0 experiment contract

`z0-v1.yaml` is the preregistered decision contract. Do not edit it after
observing results. A changed SHA makes the run ineligible for the gate.

The normal sequence is:

```bash
go run ./cmd/zeroprobe run \
  --phase z0-a \
  --profile research/preregistration/z0-v1.yaml \
  --benchmark-profile benchmarks/profiles/local-sim.yaml \
  --endpoint http://127.0.0.1:8000/v1/chat/completions \
  --artifact-root benchmarks/raw/z0

go run ./cmd/zeroprobe ingest --artifact-root benchmarks/raw/z0
go run ./cmd/zeroprobe analyze --artifact-root benchmarks/raw/z0
go run ./cmd/zeroprobe verify --artifact-root benchmarks/raw/z0
```

`run` writes and hashes the immutable manifest before the first request. The
deployed Envoy and EPP collectors must write `envoy.jsonl` and `epp.jsonl` in
the same artifact root using the normalized contracts documented in the
deployment guide. Optional normalized source observations use
`source-pressure.jsonl`. `ingest` never hides an unknown or uncorrelated line:
it copies it to `unmatched.jsonl`, and `verify` then refuses completeness.

`benchmarks/profiles/local-sim.yaml` is simulator-only. Before a real-GPU run,
replace its declared prefix token counts with values derived from the deployed
model tokenizer and measured P2P/recompute crossover. Do not label transport as
RDMA/EFA unless the Pod path was actually verified.
