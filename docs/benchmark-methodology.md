# Benchmark methodology

## Question and comparator

The primary question is whether complete group width `N` exposes a material scheduling opportunity over frozen llm-d `load-aware + P2P` routing. Arm B is the placement comparator; Arm A diagnoses prefix-affinity behavior; Arm D isolates P2P value. The offline oracle sees the same endpoint snapshot but can evaluate every feasible `k` from 1 through `min(N, healthy replicas)`.

## Preregistered factors

- Fan-out widths: 2, 4, 8, 16.
- Arrival skew: 0, 1, 5, 20 ms.
- EPP replicas: 1 and 2.
- Load: idle, moderate, near-saturation.
- Cache state: warm owner, distributed warm, cold.
- Prefix regimes: below, near, and above the measured zeroing crossover.
- Output regimes: short and moderate.
- Transport: declared TCP or EFA, never silently inferred.
- Real evidence fleet: six to eight homogeneous GPU/model replicas.

The repository profile expands deterministically and uses fixed sibling suffixes. Prompt text is sent privately to the repository's condition driver and model but intentionally excluded from evidence records. Before every real-GPU group, the driver stops prior load, resets every configured replica, probes the exact prefix and requires a zero cached-token readback, resets again, warms the declared owners, and requires at least the preregistered prefix-token count on an identical second request. It then drives isolated gateway traffic, measures successful QPS over a stabilization window, and rechecks every owner without accepting a miss that would merely re-warm it. The frozen offered and achieved load bands are idle 0–5%, moderate 40–60%, and near saturation 85–95% of measured saturation. Cold has zero cache owners, warm-owner has one, and ordinary distributed-warm has at least two. Z0-C fixes the cache cell to distributed-warm and requires its exact 1/2/4 source set to equal the observed cached set. The pinned EPP observer records per-endpoint source-prefix tokens for each actual request; source compilation requires that dispatch-time index view to equal the attested set. The group records the receipt with the controller revision, state digest, and applied timestamp; missing, partial, evicted, changed, or mislabeled state stops the run or invalidates the bundle.

## Streaming measurement

Each sibling is an independent OpenAI-compatible streaming request. A child is successful only after valid SSE framing, at least one semantic text or tool-call delta, and terminal `[DONE]`. Role-only and usage-only chunks do not define TTFT. The client bounds event size and total request duration, records the first semantic-output timestamp, drains terminal state, and preserves failure or cancellation.

- **Child TTFT:** dispatch to first content token.
- **Child latency:** dispatch to terminal stream outcome.
- **Group makespan:** earliest group dispatch to the last child terminal outcome.
- **Slowest-child TTFT:** maximum child TTFT in the group.
- **Recomputed prefix tokens:** measured only when the upstream evidence provides it.
- **Estimated prefill GPU-seconds:** a calibration-derived value, not a hardware counter.

The all-of-N objective does not hide failed siblings. Any incomplete or failed group remains in raw output and prevents a positive complete-evidence assertion.

## Z0 phases

### Z0-A — placement dispersion

Record the target vector, endpoint snapshot, excess collisions, and in-flight publication delay for both upstream routing arms across the matrix. One and two EPP replicas are observation conditions, not a claim of coordinated active-active behavior.

### Z0-B — N-aware oracle regret

Calibrate prefill throughput, pull throughput, cached bytes per token, service time, in-flight publication delay, and the affinity load gate on the chosen real stack. Hash that calibration. The pinned EPP observer runs after upstream request preparation and records the post-screener candidate set, actual upstream scores, actual target, actual selected P2P source, and source-prefix coverage. The observed Arm-B comparator forces every sibling's recorded target and acquisition/source path—even when offline recomputation would look cheaper—and does not synthesize a second local-policy baseline. Queue depth and the larger of running/published in-flight count become calibrated endpoint availability for offline oracle evaluation. A simulator opportunity must be confirmed on real GPUs.

### Z0-C — source pressure

With one, two, and four prefix sources, record selected source, concurrent pulls, transfer throughput, last-sibling TTFT, and NIC/CPU pressure when observable. This phase is independent of placement. Missing hardware counters stay missing rather than being replaced with an estimate.

## Statistical decision

Pair observations by repetition, width, prefix regime, output regime, arrival skew, load, cache state, and transport. For each deterministic bootstrap repetition, resample pair indexes, recompute baseline p95 and candidate p95, then compute `(baseline_p95 - candidate_p95) / baseline_p95`. Report the observed paired-p95 estimate and a 95% percentile confidence interval across 10,000 resamples. Each decision cell requires at least 20 pairs.

The compiler accepts exactly one complete real-GPU Z0-B bundle. It re-runs ledger verification, requires an identical deployment invariant across bundles, regenerates the oracle from raw placements plus calibration, and recomputes the placement cells. If placement does not pass, it additionally requires complete Z0-C bundles with attested prefix-source counts 1, 2, and 4; it regenerates source observations from raw groups, placements, and runtime transfer telemetry before recomputing paired cells. Retained derived files must match byte-semantic regeneration before signing. The placement branch passes only if the lower confidence bound is at least 0.10 for p95 group makespan in two adjacent widths within one load/prefix/transport regime. If placement fails, the source-pressure branch may pass when the same lower-bound threshold holds for p95 last-sibling TTFT. Otherwise the result is negative. There is no caller-provided completeness flag.

## Exclusions and retention

The frozen exclusions are an infrastructure restart outside a declared failure experiment, preregistration hash mismatch, missing raw request or placement record, or a workload cell not generated by the preregistration. Failed and negative runs are retained. Exclusion counts and reasons belong in the final report; they cannot be applied only after observing performance.

`metrics.prom` is a final Prometheus text-format snapshot and `traces.jsonl` contains only observed group/child intervals and real first-content events. Neither file is a live endpoint. The optional dashboard/rules require explicit post-run import into the operator's monitoring backend; Stage 1 does not deploy a collector or fabricate planning/queue/KV spans.

## Interpretation

Simulation validates code paths and expected qualitative behavior. Kind validates the pinned Kubernetes/EPP request path. Only the homogeneous real-GPU run can populate the performance report or authorize production coordination.
