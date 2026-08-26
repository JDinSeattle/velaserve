# Performance report

## Current result

**Real-GPU evidence: awaiting real-GPU Z0.**

No speedup, latency reduction, throughput improvement, break-even point, or gate branch is claimed. Local simulation and Kind results validate the harness, not hardware performance.

## Experiment identity

| Field | Value |
|---|---|
| Preregistration | `research/preregistration/z0-v1.yaml` |
| Baseline | Arm B: pinned upstream load-aware + P2P |
| Candidate | Offline N-aware oracle; online candidate absent |
| Model/revision | awaiting real-GPU Z0 |
| GPU/replica count | awaiting real-GPU Z0 |
| Transport/calibration hash | awaiting real-GPU Z0 |
| Artifact run IDs and ledger hashes | awaiting real-GPU Z0 |

## Placement gate table

Populate one row per declared realistic load/prefix/transport regime and width. Improvement is `(Arm B - oracle or candidate) / Arm B`; report the 95% paired-bootstrap interval and the number of valid pairs.

| Width | Regime | Arm B p95 makespan | Comparator p95 makespan | Improvement estimate | 95% CI | Pairs | Pass |
|---:|---|---:|---:|---:|---|---:|---|
| 2 | awaiting real-GPU Z0 | — | — | — | — | — | — |
| 4 | awaiting real-GPU Z0 | — | — | — | — | — | — |
| 8 | awaiting real-GPU Z0 | — | — | — | — | — | — |
| 16 | awaiting real-GPU Z0 | — | — | — | — | — | — |

## Source-pressure gate table

| Width | Prefix sources | Baseline p95 last-sibling TTFT | Comparator p95 | Improvement estimate | 95% CI | Pairs | Pass |
|---:|---:|---:|---:|---:|---|---:|---|
| 2 | awaiting real-GPU Z0 | — | — | — | — | — | — |
| 4 | awaiting real-GPU Z0 | — | — | — | — | — | — |
| 8 | awaiting real-GPU Z0 | — | — | — | — | — | — |
| 16 | awaiting real-GPU Z0 | — | — | — | — | — | — |

## Fixed plots

The final report should render these from ledger-bound records without changing axes after results are viewed:

1. p50/p95/p99 group makespan versus fan-out width, faceted by load and prefix regime.
2. p95 slowest-child TTFT versus fan-out width, faceted by number of prefix sources.
3. Predicted Arm-B/oracle regret versus width, with measured real-GPU confirmation visually separate.
4. Endpoint placement vector and excess collisions versus arrival skew/EPP replica count.
5. Recomputed prefix tokens and measured transfer throughput versus width.
6. Break-even surface across prefix size, load, width, and transport.

Simulation series must be labeled `simulation_only`; derived prefill GPU-seconds must be labeled `estimated`; unavailable NIC/CPU counters remain absent.

## Decision record

| Field | Value |
|---|---|
| Evidence completeness | awaiting real-GPU Z0 |
| Signed gate artifact | awaiting real-GPU Z0 |
| Branch | awaiting real-GPU Z0 |
| Authorized follow-up | none |

The final narrative must include failures, cancellations, exclusions, unmatched records, sensitivity to calibration, negative regions, and cost. A negative result is a valid completion outcome.
