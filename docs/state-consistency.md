# State and evidence consistency

Stage 1 avoids mutable distributed coordination. Its consistency problem is narrower: each result must remain attributable to one frozen experiment, one upstream revision set, one workload cell, and one immutable artifact bundle.

## Frozen inputs

`research/preregistration/z0-v1.yaml` fixes the seed, widths, arrival skews, EPP replica counts, arms, exclusions, thresholds, bootstrap settings, and four upstream commit SHAs. A run copies this file and the expanded benchmark profile into its artifact directory and records their hashes before requests begin. Cloud preflight compares the preregistration and calibration hashes to operator-provided expected values and checks Git-tracked inputs against `HEAD`. A real-GPU run additionally records `preflight-binding.json`: the checked-out/controller commit, exact digest-addressed images, pod specs, loaded driver ConfigMap, model revision, active arm, EPP count, restart-free pod UIDs, homogeneous GPU-node identities, transport, and both input hashes. Collection repeats preflight before touching raw artifacts and requires the complete binding—except its observation timestamp—to match, so restarts or drift during the run fail closed. The gate compiler requires the same invariant across submitted bundles.

## Identity and correlation

Every group has a run ID and group ID; every sibling has a request ID. The same IDs flow through request headers, group results, Envoy access records, EPP observations, placement events, and oracle records. The recorder joins on those stable IDs. Ambiguous, malformed, duplicated, or absent correlations are written to `unmatched.jsonl`; they are not coerced into a placement.

Before each real group, the condition controller invokes the repository driver. The driver proves reset state and full-prefix owner hits through vLLM per-request cached-token details, rechecks owners after load stabilization, and returns a non-empty observed-state object. Its load/cache/source-count labels, full payload, controller revision, timestamp, and SHA-256 must agree. For Z0-C, every dispatch-time EPP snapshot must independently expose exactly the same full-prefix source IDs. A partial/evicted prefix, changed payload, source mismatch, or receipt from another controller revision invalidates the bundle.

Each group result contains one terminal child record per declared width. Failed and cancelled children retain their reason and timing. The run-completion record distinguishes expected, selected, persisted, failed, and cancelled counts. A limited smoke run is explicitly incomplete and cannot be promoted into complete gate evidence.

## Append-only artifact rules

Artifact files use exclusive creation. `internal/artifacts.Record` accepts only canonical relative regular-file paths beneath a real artifact root, resolves symlinks, refuses a duplicate ledger path, hashes the file, records its size, and appends one JSONL entry. Verification recomputes every hash and size and rejects duplicate or non-canonical entries.

This is tamper evidence for a local bundle, not a transaction system or content-addressed object store. The ledger itself is bound into the gate decision by SHA-256; the signed decision is an Ed25519 envelope over the canonical decision JSON. Private signing keys are excluded by `.gitignore` and must remain outside the repository.

## Deterministic computation

The offline planner sorts endpoint identities before choosing the minimum predicted finish time and applies a stable identity tie-breaker. The observed Arm-B comparator does not re-optimize: it forces the recorded target and selected acquisition/source. Bootstrap sampling uses the preregistered seed and requires at least 20 paired observations. Replay records both the placement-file and calibration-file hashes. Before signing, the compiler regenerates oracle and source-pressure records from their raw inputs and rejects any retained derived mismatch.

The simulator is deterministic for a given profile and seed, but real scheduling and network timing are not expected to be bit-for-bit deterministic. Reproducibility therefore comes from frozen cells, complete raw records, paired analysis, immutable revisions, and confidence intervals rather than an identical wall-clock trace.

## What is not synchronized

Two Stage-1 EPP replicas do not share plan state. Their local in-flight views can diverge, which is exactly what Z0-A observes. No Valkey instance, lease, fencing epoch, slot claim, reservation, or production plan exists. The Helm deployment contract records `coordination_store: absent` and `production_picker: absent`.

If a future gate authorizes distributed coordination, its state protocol requires a separate design and failure proof. Stage-1 types and the offline planner do not imply that such a protocol has already been built.
