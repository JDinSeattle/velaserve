# VelaServe

Fan-out-aware scheduling research for agentic vLLM serving, built as an evidence-gated extension path around the upstream llm-d Router.

> Does knowing the complete fan-out width `N` let a scheduler choose a better replica width `k` and reduce p95 all-of-N makespan or last-sibling TTFT by at least 10% over frozen upstream `load-aware + P2P` routing?

**gate status: NOT RUN ON REAL GPU**

No production placement or source-pressure mechanism has been implemented. Stage 1 freezes the question, upstream baselines, workload, evidence format, offline oracle, local simulator, recorder, statistics, Kubernetes harness, and operator-reviewed AWS handoff. A real-GPU Z0 result decides whether this project proceeds to placement coordination, narrows to source-pressure control, or stops with a negative result.

## Cloud qualification results

These results follow the project owner’s separately run cloud qualification recorded in the experience bank. The workload, environment, denominators and limitations below belong to that round; local regression checks for this checkout are separate. [Result record](docs/experience-bank-results.json).

- Cut cumulative client-side parser allocation by 87.1% (8,800,000 to 1,135,200 B/op) in a local CPU microbenchmark by adding an explicit no-body-retention mode for measurement callers that reuses a bounded event buffer while still running the same SSE boundary, JSON and error checks; the workload was 256 synthetic 4,096-byte content events with usage and DONE on Go 1.26.6 with identical compile flags on both sides.

- Reported the same-round full-text control at 8,760,000 B/op, so most of the gain comes from explicitly changing the output-retention contract for the measurement API; the result must not be stated as 'full text is also 87.1% faster' or as a model-throughput gain.

- Preserved failure measurements: failed requests keep an observed first-token time and usage, and requests with no observed first token are left absent instead of zero-filled; truncated UTF-8, oversized event, missing DONE and cancelled read-stream injections (100 each, 400 total) all returned the specified errors with no hanging goroutines.

- Checked parser correctness against a handwritten expected-event table - type, content length, first-content-event position, usage and terminal state item by item - rather than comparing two result summaries.

- Ran the parser comparison as six new paired processes in AB/BA order, 100 warmups and 1,000 measured iterations per side per process, with input generation outside timing and parse plus cleanup inside timing, then took the median of the six per-process B/op values; hardware was a single 8-core x86 CPU with 32 GiB and no GPU, microbenchmark single-goroutine, fault tests at concurrency 32.

## Checkout validation

The measurement client now selects `DiscardText`, retains valid partial timing and usage on stream errors, and omits unobserved TTFT from child JSON and metric samples. Independent review also corrected payload-size admission at 64 KiB and larger (SSE framing is excluded from the payload limit), made usage snapshots atomic, and cleared cache counts when a later usage snapshot omits cache details. An invalid usage frame leaves the previous valid observation intact, so failed requests remain valid failure evidence.

`go test ./... -race` passed after these changes, including exact-size LF/CRLF events, malformed usage after valid output, cancellation after observed output, and JSON-schema compatibility. This local regression run does not reproduce or replace the cloud allocation measurements above.

## What exists now

- A preregistered Z0-A/B/C experiment with immutable upstream Git SHAs and a fixed 10% practical-significance threshold.
- A Go best-of-N streaming client that validates OpenAI-compatible SSE through semantic output and `[DONE]`, and records every child, including failures and cancellations.
- An eight-endpoint deterministic simulator for pipeline calibration only. Simulator output is always `simulation_only` evidence.
- A deterministic N-aware offline oracle that evaluates local hit, P2P pull, and recompute cost against the same endpoint snapshots as the upstream arms.
- Envoy/EPP record correlation, repository-provided cache/load conditioning, condition-attested groups, strict model-runtime P2P transfer joins, append-only SHA-256 artifact ledgers, paired-p95 bootstrap confidence intervals, and a ledger-derived gate compiler.
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
| Real-GPU deployment handoff | Any GPU scheduling-performance claim |

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
cloud-calibrate-crossover.sh -> cloud-freeze-profile.sh -> apply measured threshold
  -> cloud-calibrate-load.sh -> oracle-calibration -> enable condition driver
  -> cloud-preflight.sh -> condition-attested cloud-run-z0.sh
  -> cloud-collect.sh -> cloud-cleanup.sh
```

The calibration helpers open their own per-Pod tunnels and derive the routing threshold, workload token regimes, load profiles, and oracle cost model from retained raw observations. Preflight then checks AWS identity, region, exact EKS cluster, GPU quota, the six-to-eight replica bound, one-GPU requests/limits, required vLLM cache/readback flags, restart-free homogeneous model pods, node/runtime/GPU-driver identity, model/controller revisions, exact image digests and pod-spec hashes, deployed route/router contracts, absence of model HPA, calibration bindings, transport, bucket, kubectl context, and endpoint reachability. Every real group requires a condition-driver receipt backed by exact cache-token and load readback. Collection re-runs preflight to prove start/end deployment identity, normalizes the raw observer streams, verifies the artifact ledger, and uploads to a unique S3 run prefix.

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
| `internal/conditioncontroller`, `internal/conditiondriver` | Fail-closed attestation plus concrete cache/load conditioning for real workload state |
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
