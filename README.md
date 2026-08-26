# VelaServe

Fan-out-aware scheduling research for agentic vLLM serving, built as an evidence-gated extension path around the upstream llm-d Router.

> Does knowing the complete fan-out width `N` let a scheduler choose a better replica width `k` and reduce p95 all-of-N makespan or last-sibling TTFT by at least 10% over frozen upstream `load-aware + P2P` routing?

**gate status: NOT RUN ON REAL GPU**

No production placement or source-pressure mechanism has been implemented. Stage 1 freezes the question, upstream baselines, workload, evidence format, offline oracle, local simulator, recorder, statistics, Kubernetes harness, and operator-reviewed AWS handoff. A real-GPU Z0 result decides whether this project proceeds to placement coordination, narrows to source-pressure control, or stops with a negative result.

## What exists now

- A preregistered Z0-A/B/C experiment with immutable upstream Git SHAs and a fixed 10% practical-significance threshold.
- A Go best-of-N streaming client that validates OpenAI-compatible SSE through semantic output and `[DONE]`, and records every child, including failures and cancellations.
- An eight-endpoint deterministic simulator for pipeline calibration only. Simulator output is always `simulation_only` evidence.
- A deterministic N-aware offline oracle that evaluates local hit, P2P pull, and recompute cost against the same endpoint snapshots as the upstream arms.
- Envoy/EPP record correlation, condition-attested groups, strict model-runtime P2P transfer joins, append-only SHA-256 artifact ledgers, paired-p95 bootstrap confidence intervals, and a ledger-derived gate compiler.
- Pinned Helm/Kind manifests that build and exercise the exact llm-d-router commit through Envoy Gateway and Gateway API Inference Extension.
- Terraform for a dedicated EKS experiment environment, immutable ECR repositories, a versioned encrypted S3 evidence bucket, Karpenter prerequisites, and scale-to-zero GPU capacity. Repository scripts never apply or destroy Terraform.

The local simulator proves protocol, correlation, artifact, and analysis plumbing. It does **not** establish GPU performance, a gate outcome, or a production speedup.

## Stage-1 boundary

| Present | Deliberately absent until a real-GPU gate passes |
|---|---|
| Frozen upstream Arm A and Arm B | Valkey or another coordination store |
| Fan-out headers and benchmark client | Cross-EPP plan creation or slot claims |
| Offline N-aware oracle | Production target override |
| Simulation-only zeroing | Source budgets or dispatch waves |
| Real-GPU deployment handoff | Any performance claim |

Ordinary requests remain an upstream concern. VelaServe does not replace vLLM, llm-d Router, Envoy, Gateway API, Kubernetes discovery, KV indexing, or peer-to-peer KV transfer.

## Reproduce locally

Prerequisites are Go 1.26.6, Docker Engine, `kubectl`, `curl`, `jq`, and network access for the first pinned-tool/upstream fetch. The bootstrap script downloads checksum-verified Helm, Kind, and Terraform binaries into ignored `.tools/` state.

Run the complete AWS-independent audit:

```bash
bash scripts/verify-stage1.sh
```

It runs formatting, vet, unit/property/integration/static tests, the race detector, JSON-schema fixtures, a verified simulation-only Z0 bundle, Grafana JSON validation, Helm rendering, Terraform validation, public-source safety checks, and a clean Kind Arm-B SSE smoke. It writes `.tools/verification.json`, including whether Docker/Kind actually ran. An explicit `VELASERVE_ALLOW_NO_DOCKER=1` waiver is available for environments with no Docker Engine; a waiver is never equivalent to a Kind pass.

For only the local upstream routing smoke:

```bash
./hack/kind-up.sh arm-b
./hack/kind-down.sh
```

The first command applies the small observational patch to the exact pinned llm-d Router source, creates an exact Kind cluster, installs the pinned Gateway components, deploys eight simulator endpoints, and verifies a complete fan-out SSE stream through the generated Envoy Gateway service plus an emitted EPP scheduling record. The second command deletes only the `velaserve-z0` Kind cluster and its generated port-forward state.

## Run the real-GPU handoff

The repository performs no AWS mutation on its own. The operator reviews the Terraform plan, applies infrastructure manually, deploys six to eight homogeneous vLLM replicas behind the declared `velaserve-model` service, pushes digest-addressed images, and then runs the guarded sequence:

```text
cloud-preflight.sh -> condition-attested cloud-run-z0.sh -> export raw EPP + Envoy records
                   -> cloud-collect.sh -> cloud-cleanup.sh
```

The preflight checks AWS identity, region, exact EKS cluster, GPU quota, the six-to-eight replica bound, one-GPU requests/limits, restart-free homogeneous model pods, model/controller revisions, exact model/EPP/controller image digests, deployed routing/EPP contracts, absence of model HPA, preregistration/calibration hashes, transport, bucket, kubectl context, and endpoint reachability. Every real group requires a condition-controller receipt that matches its load, cache, and (for Z0-C) source-count state. Collection normalizes only the marker emitted by the pinned EPP observer, verifies the artifact ledger, and uploads to a unique S3 run prefix.

Follow [the cloud handoff runbook](docs/cloud-handoff.md) rather than invoking these scripts from partial configuration.

## Evidence and decision rules

- Primary placement metric: p95 group makespan.
- Source-pressure metric: p95 last-sibling TTFT.
- Pairing key: repetition, width, prefix/output regime, arrival skew, load, cache state, transport, and source-count condition when applicable.
- Inference: 10,000 deterministic paired bootstrap resamples; every resample recomputes both p95 values before calculating relative improvement, with a 95% percentile interval.
- Placement branch: the lower confidence bound is at least 10% in two adjacent widths under a declared realistic regime.
- Source-pressure branch: placement fails, while the independent source-pressure threshold passes.
- Otherwise: publish the negative result and stop coordination work.

Incomplete groups remain in raw evidence and make the bundle ineligible for a positive gate. Calibration parameters and estimated prefill GPU-seconds are labeled as derived values, never hardware counters.

## Repository map

| Path | Responsibility |
|---|---|
| `research/preregistration/` | Frozen hypotheses, matrix, exclusions, thresholds, and upstream pins |
| `cmd/fanoutbench` | OpenAI-compatible best-of-N streaming workload |
| `cmd/zeroprobe` | Run, ingest, analyze, and verify lifecycle |
| `research/placement-recorder` | Envoy/EPP correlation into placement evidence |
| `research/oracle-replay` | Offline N-aware replay against observed snapshots |
| `research/gate-decision` | Paired analysis and signed decision artifact |
| `research/gate-compiler` | Re-verifies raw real-GPU bundles and derives the only signable decision |
| `internal/conditioncontroller` | Fail-closed driver/attestation contract for real workload state |
| `internal/artifacts` | Append-only artifact hashing and verification |
| `deploy/` | Helm, Gateway, observability, and Kind assets |
| `infra/terraform/` | Review-first AWS/EKS experiment infrastructure |
| `docs/` | Architecture, methodology, failures, gate status, and handoff |

## Documentation

- [Architecture](docs/architecture.md)
- [State and evidence consistency](docs/state-consistency.md)
- [Benchmark methodology](docs/benchmark-methodology.md)
- [Failure analysis](docs/failure-analysis.md)
- [Performance report](docs/performance-report.md)
- [AWS cloud handoff](docs/cloud-handoff.md)
- [Upstream version matrix](docs/upstream-version-matrix.md)
- [Gate status](docs/gate-status.md)

## License and security

The code is available under the [MIT License](LICENSE). See [CONTRIBUTING.md](CONTRIBUTING.md) for evidence-preserving changes and [SECURITY.md](SECURITY.md) for private vulnerability reporting guidance.
