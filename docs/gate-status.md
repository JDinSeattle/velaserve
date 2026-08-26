# Gate status

## Current state

**NOT RUN ON REAL GPU — insufficient evidence.**

No production placement or source-pressure coordination code exists. The repository contains an offline planning oracle, benchmark/evidence tooling, local simulation, pinned upstream routing manifests, and an operator-reviewed cloud handoff. None of those can override a live endpoint or coordinate siblings across EPP replicas.

## Completion checklist

| Requirement | State |
|---|---|
| Frozen Z0 preregistration and upstream SHAs | complete |
| Versioned schemas and positive/negative fixtures | complete |
| Append-only artifact ledger and verification | complete |
| Deterministic simulator and local verified bundle | complete, `simulation_only` |
| Pinned llm-d Router Kind/Envoy SSE smoke | complete locally |
| Pinned llm-d observational adapter and raw-log normalizer | complete locally |
| Condition driver/controller, per-group receipts, and strict P2P transfer join | complete; environment adapters are operator-supplied |
| Offline N-aware replay and paired-p95 statistics | complete |
| Ledger-derived gate compiler and tamper-bound signing | complete locally |
| Real-GPU calibration | not run |
| Six-to-eight-replica Z0-A/B/C evidence | not run |
| Complete ledger-bound gate cells | not run |
| Signed real-GPU gate decision | not run |
| Authorized production coordination branch | none |

## Branch rules

- `placement` requires complete real-GPU evidence whose 95% confidence lower bound is at least 10% for p95 group makespan in two adjacent widths within one realistic regime.
- `source-pressure` is considered only if placement fails and the independent last-sibling TTFT rule passes.
- `negative-result` follows when complete evidence supports neither mechanism.
- `insufficient-evidence` is the only current branch and cannot be signed as a final decision.

The threshold, adjacency rule, pairing key, seed, exclusions, and upstream pins cannot be changed after inspecting results. A partial smoke, simulation result, missing correlation, mixed fleet, or incomplete artifact bundle cannot authorize either implementation branch.

## Forbidden before the gate

- Valkey or another shared plan store.
- Group plan creation in the online EPP path.
- Request-to-slot claiming, leases, epochs, or reservations.
- Production target override using the offline planner.
- P2P source budgets, dispatch waves, or a broadcast tree.
- Performance claims based on simulator output.

When the operator completes the AWS run, commit the raw artifact references, derived gate cells, unsigned decision, signed decision envelope, public verification key, failure/exclusion accounting, and performance report before writing a follow-up implementation plan.
