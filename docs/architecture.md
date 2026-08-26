# Architecture

## Current Stage-1 data path

```text
fanoutbench
  | OpenAI-compatible streaming requests; declared group ID, request ID, N
  v
Envoy Gateway / Gateway API Inference Extension
  v
pinned upstream llm-d EPP (Arm A or Arm B)
  v
homogeneous model endpoints (local simfleet or operator-deployed vLLM)

condition-attested groups.jsonl + Envoy JSONL + pinned-observer EPP log
  v
EPP normalizer + placement-recorder -> placements.jsonl + unmatched.jsonl
  v
offline replay (observed Arm B and N-aware oracle)
  v
raw-bundle gate compiler -> hash-bound decision -> Ed25519-signed gate artifact
```

The online request path contains only upstream routing. The N-aware planner is offline research code: it cannot select a live endpoint, create distributed state, or alter an inference request. That separation is the central Stage-1 safety boundary.

## Components

| Component | Role | Evidence boundary |
|---|---|---|
| `fanoutbench` | Sends N sibling streams with deterministic IDs/skew and waits for all terminal outcomes | Records child TTFT, latency, completion, usage, and group makespan |
| condition controller + driver | Resets and zero-probes every replica, warms the exact private prefix on deterministic owners, requires full-prefix cached-token readback, drives/measures background load, and rechecks owners | Records the revision/SHA-bound load, cache, and source-count receipt; prompt text and credentials are never retained |
| `simfleet` | Models eight deterministic endpoints and emits normalized routing records | Always labeled `simulation_only`; not a performance proxy |
| llm-d EPP | Runs frozen precise-affinity + P2P or load-aware + P2P profiles | Exact commit plus a reviewable observational-only patch that emits candidate snapshots, target, selected source, and per-endpoint indexed source-prefix coverage |
| `placement-recorder` | Joins request/group IDs across result, Envoy, and EPP records | Preserves unmatched inputs instead of inventing a target |
| source-pressure compiler | Joins group, EPP placement, and one raw model-runtime acquisition event for every Z0-C child | Requires runtime source = EPP-selected source, transfer completion before first content, and exact dispatch-time EPP index = attested source set |
| N-aware oracle | Evaluates feasible endpoint allocation from the observed snapshot | Prediction depends on a hashed, operator-supplied calibration |
| gate compiler | Re-verifies ledgers, invariant deployment bindings, raw-to-derived equality, matrix completeness, sequential Z0-B/Z0-C eligibility, confidence, threshold, and adjacency | Only compiler output can enter the signing command |
| artifact ledger | Records a canonical relative path, byte count, SHA-256, and timestamp | Duplicate paths, traversal, symlinks, and later mutation fail verification |

## Routing arms

- **Arm A — affinity + P2P:** pinned upstream precise-prefix affinity plus load gates and peer KV source selection.
- **Arm B — load-aware + P2P:** pinned upstream load-aware target selection plus the same peer KV capability. This is the preregistered placement comparator.
- **Arm D — load-aware without P2P:** control arm for measuring the value of peer reuse.
- **Offline oracle:** deterministic N-aware assignment over the same snapshot. It is not an online arm.

The chart permits only the two Stage-1 upstream routing profiles. A second EPP replica is allowed solely for dispersion observation and carries an explicit warning that EPP-local state is not synchronized.

## Local topology

`hack/kind-up.sh` creates the exact `velaserve-z0` Kind cluster, applies the checked-in observer patch to the pinned EPP source in a temporary build tree, installs pinned Envoy Gateway and Gateway API Inference Extension manifests, deploys eight simulator pods, waits for HTTPRoute `Accepted` and `ResolvedRefs`, and verifies a fan-out SSE response through the generated Envoy Gateway service plus an observer record. Scratch images contain only statically linked binaries. The local chart runs non-root, drops capabilities, disables service-account token mounting, and uses a read-only root filesystem.

## AWS handoff topology

Terraform describes a dedicated VPC, private worker subnets, EKS, a two-node CPU system group, Karpenter, immutable ECR repositories, an encrypted/versioned S3 evidence bucket, and Pod Identity scoped to `runs/*`. The GPU NodePool is scale-to-zero and bounded to six through eight GPUs for the real benchmark.

The operator owns the model deployment. It must expose six to eight homogeneous vLLM pods with label `app.kubernetes.io/name=velaserve-model`, stable per-pod chat/reset URLs, service `velaserve-model` on port 8000, automatic prefix caching, prompt-token-detail responses, an immutable model revision, and digest-addressed images. The support chart deploys the repository's condition driver and controller from the same immutable VelaServe image and rolls the driver when its endpoint ConfigMap changes. `aws-z0-values.yaml` disables simfleet and points the EPP only at the declared real service/selector.

`metrics.prom` and `traces.jsonl` are immutable per-run files, not live scrape or OTLP endpoints. The Prometheus rules and Grafana dashboard are optional post-import assets for an operator who loads those records into an observability backend; Stage 1 does not deploy Prometheus, Grafana, or an OpenTelemetry Collector and does not claim live monitoring.

## Conditional future branch

Only a verified, signed real-GPU decision may authorize one of these mutually exclusive plans:

1. `placement`: design active-active-safe group plans and slot claiming.
2. `source-pressure`: leave target placement upstream and design only the smallest measured pull-pressure control.
3. `negative-result`: implement neither mechanism and publish the boundary.

The future design is intentionally not present in current production code.
