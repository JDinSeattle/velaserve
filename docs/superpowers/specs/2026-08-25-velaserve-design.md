# VelaServe

## Fan-Out-Aware Scheduling for Agentic vLLM Serving

**Status:** Final project specification, version 3  
**Date:** 2026-08-25  
**Primary language:** Go  
**Primary environment:** AWS EKS  
**Inference engine:** vLLM  
**Routing foundation:** llm-d Router / Endpoint Picker (EPP)

---

## 1. Executive Decision

VelaServe is a focused ML-infrastructure project for serving agentic LLM workloads. It does not build a general MaaS platform and does not replace vLLM, Envoy, Kubernetes Gateway API, or llm-d Router.

The project investigates and, only if justified by a pre-registered research gate, extends the llm-d Router with **declared fan-out semantics**. When an agent launches a best-of-N, tree-search, rollout, or subagent group, VelaServe makes the otherwise missing group width `N` available to scheduling. It can then choose how many replicas `k` should serve the group, create one shared placement plan, reserve sibling work against predicted endpoint availability, and reuse the existing llm-d peer-to-peer KV pull path.

The project answers one falsifiable systems question:

> Does knowing the complete fan-out width `N` allow a group planner to choose a better replica width `k` and reduce slowest-child latency, total makespan, or repeated prefill work compared with current per-request `affinity + P2P` and `load-aware + P2P` routing?

No production coordination code is written until the upstream baseline passes a pre-registered zeroing experiment. If the measured opportunity is real, the primary artifact is a Go systems contribution with:

- an llm-d EPP extension;
- a cross-EPP fan-out state machine;
- a deterministic group-placement algorithm;
- fault-tolerant fallback to the upstream routing profile;
- a reproducible real-GPU benchmark and break-even analysis.

If group placement has no material opportunity but shared-prefix pulls overload their sources, the project narrows to group-aware P2P source-pressure control. If neither opportunity exists, the implementation is terminated and the negative result is published. No performance improvement is assumed in advance.

---

## 2. Problem Statement

Agentic inference frequently creates multiple requests that share most of their prompt:

- best-of-N candidate generation;
- tree-search expansion;
- parallel tool planning;
- RL rollout branches;
- subagent fan-out;
- multi-agent fork/join stages.

For a group of `N` siblings with the same large prefix, two goals conflict:

1. **Prefix affinity** prefers the replica that already owns the prefix KV cache.
2. **Group makespan** prefers distributing siblings across available replicas so the slowest child finishes sooner.

Existing llm-d routing already solves much of the per-request problem:

- precise or approximate prefix-cache awareness;
- queue, active-request, token-load, and KV-utilization signals;
- prefix affinity with load gates;
- latency-aware scoring;
- P2P KV pulls from a peer cache;
- load-aware placement with P2P fallback.

Those capabilities remain request-centric. Local in-flight accounting improves sequential placement decisions, but it does not give an individual decision the group's width `N`, the set of sibling assignments that will follow, or the group's maximum-completion-time objective. Cross-EPP dispatches and genuinely concurrent decisions may also observe different state.

For one request, greedily reusing a warm replica until it is saturated may be correct. For a group that completes only when its slowest sibling completes, the correct number of replicas `k` depends jointly on `N`, endpoint availability, predicted service time, prefix residency, and the cost of obtaining the prefix elsewhere. A per-request scheduler does not possess the complete information needed to choose that split once for the group.

VelaServe adds only that missing semantic layer.

---

## 3. Project Thesis

VelaServe will test the following hypotheses.

### H1: Group width contains actionable scheduling information

For at least one realistic shared-prefix operating region, an offline planner that knows `N` will choose a different replica width `k` from current per-request routing and achieve a materially lower predicted or measured group makespan.

### H2: Group planning improves tail makespan or repeated prefill work in a bounded region

When all of the following are true, group-aware placement should improve p95 or p99 fan-out makespan:

- the common prefix is long enough that recomputation is expensive;
- P2P pull is cheaper than recomputation on the deployed transport;
- multiple healthy replicas have available decode capacity;
- sibling arrivals occur close enough together to contend;
- the group width is large enough for the `k` decision to matter.

### H3: P2P source pressure may create a second group-level opportunity

When many siblings pull the same prefix, waiting-queue-aware source selection may still miss pull-serving NIC or CPU-tier pressure. This is an experimental hypothesis, not an assumed defect. Group-aware source budgets or dispatch waves enter implementation scope only if source-pressure measurements show a material last-sibling penalty.

### H4: The mechanism has an explicit break-even boundary

At low fan-out, low load, short prefixes, or slow KV transport, coordination overhead may equal or exceed the benefit. The project must publish that boundary instead of hiding it.

### H5: Ordinary chat traffic remains on the upstream path

Requests without valid fan-out metadata must bypass group state and use the unmodified llm-d routing profile. Valkey failure, stale plans, and missing siblings must degrade to upstream behavior rather than fail inference.

### 3.1 Pre-implementation zeroing gate

The following experiments run against the pinned upstream stack before the Valkey state machine or EPP picker is implemented. Baselines, thresholds, workload definitions, and exclusion rules are frozen before results are inspected.

#### Z0-A: Placement dispersion

Measure the actual sibling endpoint distribution under `load-aware + P2P` for:

- fan-out width `N in {2, 4, 8, 16}`;
- one and two EPP replicas;
- sibling arrival skew of `0`, `1`, `5`, and `20` milliseconds;
- an eight-endpoint simulator fleet;
- idle, moderate, and near-saturation background load.

Record the complete placement vector, excess collisions relative to an offline group assignment, endpoint-state snapshots, and the delay from scheduling decision to in-flight state publication. Collision rate is diagnostic evidence, not the pass/fail criterion by itself.

#### Z0-B: N-aware oracle regret

Replay the same endpoint snapshots through an offline oracle that knows `N` and evaluates every feasible replica width:

```text
k in [1, min(N, healthy_replicas)]
```

Compare the upstream decision with the oracle using predicted group makespan, slowest-child TTFT, and repeated prefix computation. Confirm any apparent simulator opportunity with a minimal real-GPU baseline run before implementing distributed coordination.

The placement project continues only when the lower bound of the 95% confidence interval shows at least a 10% p95 group-makespan improvement over Arm B in two adjacent fan-out widths under a declared realistic load regime. This practical-significance threshold may be changed only before the first result is viewed.

#### Z0-C: P2P source pressure

Place the shared prefix on one, two, and four candidate sources and increase `N`. Measure source selection, concurrent pulls per source, transfer throughput, source NIC and CPU-tier pressure where observable, and last-sibling TTFT.

If Z0-B fails but Z0-C independently meets the same pre-registered 10% practical-significance threshold for last-sibling TTFT, VelaServe pivots to the narrower problem of group-aware P2P source-pressure control while leaving target placement upstream. A broadcast tree is not assumed; the smallest effective source budget or dispatch-wave mechanism is preferred.

#### Gate outcomes

| Evidence | Decision |
|---|---|
| Z0-B passes | implement the N-aware group planner and distributed slot coordination |
| Z0-B fails, Z0-C passes | implement only group-aware P2P source-pressure control |
| both fail | stop the coordination implementation and publish the negative result |

The gate decision and all raw artifacts are committed before implementation begins. Later benchmark results cannot redefine the baseline or retroactively alter the gate.

---

## 4. Upstream Boundary

### 4.1 Reused without reimplementation

VelaServe uses the following upstream mechanisms as foundations:

- vLLM OpenAI-compatible streaming inference;
- vLLM automatic prefix caching and KV events;
- vLLM OffloadingConnector and the llm-d P2P KV-sharing path;
- llm-d Router proxy and EPP protocol handling;
- Kubernetes endpoint discovery and health tracking;
- precise prefix-cache producer and global KV index;
- `p2p-source-producer` source selection;
- llm-d token-aware and load-aware scoring;
- Gateway API / Envoy request forwarding;
- llm-d and vLLM Prometheus metrics.

### 4.2 Conditional original contribution

If Z0-B passes, VelaServe contributes:

- a minimal fan-out metadata contract;
- an active-active-safe group state machine;
- atomic fan-out plan creation;
- sibling slot reservation and idempotent claiming;
- a fan-out-aware EPP profile and picker;
- planned target validation and bounded replanning;
- group-level metrics and traces;
- a best-of-N benchmark that measures group makespan rather than only request latency.

If only Z0-C passes, the contribution is deliberately narrower:

- declared fan-out metadata that exposes total pull demand;
- group-aware accounting of planned pulls per source;
- a bounded source-concurrency budget or dispatch-wave policy;
- measured last-sibling TTFT and source-pressure evidence against upstream queue-aware source selection.

### 4.3 Explicitly not claimed as novel

VelaServe must not claim that it invented:

- prefix-cache-aware routing;
- load-aware routing;
- KV event indexing;
- peer-to-peer KV transfer;
- SLO or latency scoring;
- heterogeneous accelerator optimization;
- Kubernetes inference gateways.

The novelty claim is limited to the evidence-supported branch: **N-aware group placement** or **group-aware P2P source-pressure control** on top of upstream mechanisms.

---

## 5. Scope

### 5.1 Always-required scope

1. A pinned upstream llm-d Router and vLLM baseline.
2. The Z0-A, Z0-B, and Z0-C zeroing experiments.
3. A Go simulator, oracle replay tool, and benchmark driver.
4. One text-generation model served by a homogeneous vLLM fleet.
5. OpenAI-compatible streaming requests.
6. One workload family: best-of-N shared-prefix generation.
7. Precise prefix-cache routing and P2P KV sharing.
8. Local simulation and AWS EKS real-GPU validation.
9. Frozen benchmark definitions, raw results, and a signed gate decision.

### 5.2 Conditional placement scope

Only if Z0-B passes:

1. Two active EPP replicas for distributed-state validation.
2. Valkey as the narrow fan-out coordination store.
3. A Go EPP extension compiled into a pinned llm-d Router build.
4. N-aware plan creation and idempotent internal slot claiming.
5. One node-loss and one state-store-failure experiment.
6. Fail-open fallback to the pinned upstream profile.

### 5.3 Conditional source-pressure scope

Only if Z0-C passes:

1. Measurement of planned and active pulls per source.
2. A calibrated per-source pull budget or two-to-three-wave dispatch policy.
3. Fail-open fallback to upstream P2P source selection and recomputation.
4. No vLLM engine fork and no mandatory broadcast tree.

### 5.4 Non-goals

The following are deliberately excluded:

- general session-graph orchestration;
- critical-path scheduling for arbitrary multi-agent DAGs;
- long-horizon session pinning and eviction policy;
- a complete agent framework or MCP platform;
- a new Envoy/ext-proc implementation;
- a new service-discovery or health-check subsystem;
- heterogeneous GPU or cost-aware scheduling;
- prefill/decode disaggregation as a required topology;
- a full proactive KV-prefetch API inside vLLM;
- distributed training, QLoRA pipelines, Ray, Kubeflow, Kueue, or MLflow;
- multimodal, embedding, image, and batch APIs;
- KServe, Istio Ambient, or Dynamic Resource Allocation;
- OIDC, general RBAC, audit products, chargeback, or reporting;
- multi-cloud portability;
- a web console;
- an unmeasured broadcast tree or custom KV transport;
- treating estimated FLOPs as measured hardware counters.

### 5.5 Stretch scope

Only after the core benchmark is complete:

- pre-declaring a fan-out group before sibling arrival;
- proactive KV prefetch if a stable upstream engine hook exists;
- multi-agent fork/join critical-path scheduling;
- LoRA-aware fan-out;
- P/D-disaggregated fan-out;
- an upstream llm-d Router PR;
- hierarchical KV propagation, but only after source-pressure evidence shows bounded waves are insufficient.

Stretch work is not part of the definition of done.

---

## 6. Target Role Coverage

| Role signal | VelaServe evidence |
|---|---|
| Go backend engineering | zeroing probe, replay oracle, benchmark client, and gate-approved EPP extension |
| Distributed systems | conditional idempotency, leases, fencing epochs, TTL cleanup, and active-active EPP coordination |
| Cloud-native infrastructure | EKS, Kubernetes Gateway API, Envoy, Helm, Terraform, Karpenter |
| ML infrastructure | vLLM metrics, KV-cache behavior, prefix reuse, P2P transfer, GPU benchmarking |
| Intelligent routing | N-aware oracle plus evidence-gated group placement or source-pressure control |
| Streaming and latency | SSE correctness, TTFT, inter-token latency, makespan, scheduler overhead |
| Reliability | fail-open routing, stale-plan validation, endpoint loss, EPP restart, Valkey outage |
| Agent/MCP | best-of-N and subagent-style declared fan-out workload |
| Performance engineering | controlled experiment design, break-even surface, confidence intervals |

The project does not cover every responsibility in a MaaS platform. It produces deep, interview-defensible evidence for the inference-routing, cloud-native, high-throughput, streaming, stability, and Agent portions of the role.

---

## 7. Architecture

### 7.1 Logical request path

```text
Agent / fanoutbench
  -> Envoy / Kubernetes Gateway API
  -> llm-d Router EPP
       -> ordinary request: upstream token-aware profile
       -> declared fan-out request: VelaServe profile
            -> parse group hint
            -> placement branch, only if Z0-B passed:
                 create or load FanoutPlan in Valkey
                 atomically map RequestID to an internal slot
                 validate assigned vLLM endpoint
            -> source-pressure branch, only if Z0-C passed:
                 enforce the planned source budget or dispatch wave
            -> existing p2p-source-producer chooses or validates KV source
  -> vLLM target replica
       -> local KV hit, peer KV pull, or ordinary recomputation
  -> streaming response
```

### 7.2 Deployables

| Deployable | Ownership | Purpose |
|---|---|---|
| Envoy / Gateway | upstream | proxy, streaming, connection handling |
| llm-d EPP, 2 replicas | upstream plus VelaServe build | request parsing, scheduling, fan-out extension |
| vLLM model servers | upstream | token generation and KV cache |
| Valkey | conditional VelaServe dependency | short-lived plan, claim, or source-budget state after a gate passes |
| Prometheus / Grafana / OTel Collector | upstream ecosystem | metrics, traces, experiment evidence |
| `fanoutbench` | VelaServe | group-aware workload generation and result capture |

There is no general VelaServe API server or platform controller in the core architecture.

### 7.3 When Valkey is justified

The precise KV index does not require shared storage: each EPP replica independently consumes vLLM KV events and converges. Fan-out plans differ because they contain mutable coordination state. Two EPP replicas must not independently create conflicting sibling assignments.

Valkey is introduced only after a zeroing gate proves that cross-request coordination has material value. It is used only for:

- plan creation with compare-and-set semantics;
- sibling slot claims;
- request-id deduplication;
- fencing epochs;
- bounded TTL cleanup.

It does not store prompts, model weights, KV blocks, request bodies, or long-term telemetry. If both gates fail, Valkey is not part of the project.

---

## 8. Fan-Out Metadata Contract

### 8.1 Existing identity hint

The common prefix should use the application's existing cache identity where available, such as `prompt_cache_key`. When the deployed client or parser does not provide one, `fanoutbench` derives a stable digest from the rendered common prefix.

The digest is an identity hint, not proof that a particular vLLM replica currently contains the KV blocks. The precise KV index remains authoritative for residency.

### 8.2 Minimal declared-fan-out extension

The prototype adds exactly three versioned request headers:

```http
X-Vela-Fanout-Version: 1
X-Vela-Fanout-Group: 01J...
X-Vela-Fanout-Width: 8
```

Rules:

- group ID must be an opaque, unguessable identifier;
- width is bounded by configuration;
- all siblings in one group must use the same prefix identity;
- version 1 requires the same model, decoding limits, and `max_tokens` across siblings;
- every sibling must carry the existing request ID used by the pinned llm-d request path;
- the store atomically maps that request ID to one internal slot;
- invalid or incomplete metadata disables VelaServe behavior and falls back;
- headers are removed before forwarding to the public model endpoint unless required by a downstream VelaServe component.

The coordination deadline is derived from the existing request deadline and a server-side maximum. It is not another VelaServe header.

### 8.3 Why the contract remains small

The project does not define a session graph, tool protocol, task language, or agent framework. It communicates only the information unavailable to a request-centric scheduler: these requests are siblings and this is the group width. Stable request identity reuses the existing request ID rather than adding a VelaServe ordinal contract.

---

## 9. EPP Extension Design

### 9.1 `fanout-metadata-producer`

Responsibilities:

- parse and validate the versioned headers;
- read the prefix identity hint;
- derive a request fingerprint for idempotency;
- attach `FanoutContext` to request-scoped plugin state;
- emit no Valkey request when fan-out metadata is absent.

Output:

```go
type FanoutContext struct {
    GroupID       string
    Width         uint32
    PrefixKey     string
    RequestID     string
    Deadline      time.Time
    Protocol      uint16
}
```

### 9.2 `fanout-profile-handler`

Responsibilities:

- select the VelaServe scheduling profile only for a valid `FanoutContext`;
- select the pinned upstream token-aware/P2P profile for all other requests;
- immediately fall back when coordination is unavailable or disabled.

The ordinary path must not call Valkey.

### 9.3 `fanout-aware-picker`, placement branch only

Responsibilities:

- read healthy candidates and their upstream-produced endpoint attributes;
- create a deterministic group plan when no valid plan exists;
- atomically publish that plan;
- load the winning plan if another EPP replica published first;
- atomically assign the request ID to one unclaimed internal slot;
- return the same slot for every retry with the same request ID;
- validate the assigned target against the current candidate set;
- perform one bounded slot replan if the target is no longer eligible;
- return the chosen target to the existing EPP pipeline.

The picker does not implement endpoint discovery, health checks, KV indexing, or peer-source selection.

### 9.4 Existing `p2p-source-producer`

After VelaServe chooses the target, the existing llm-d producer selects a peer source with a better cached prefix when the measured pull threshold is satisfied. Its existing waiting-queue-aware sampling remains the baseline. If no valid source exists, the request proceeds with a local hit or recomputation.

In the placement-only branch, VelaServe coordinates **where siblings compute** while upstream llm-d remains responsible for **where transferable KV comes from**.

If Z0-C passes, a narrow VelaServe hook may constrain the eligible source set or delay a slot into a bounded dispatch wave. It must not replace the upstream KV index, transport, or pull protocol.

### 9.5 `fanout-source-budget-filter`, source-pressure branch only

Responsibilities:

- use a small pinned-router adapter to expose the candidate set immediately before upstream weighted source selection;
- exclude sources whose measured pull-concurrency budget is exhausted;
- atomically reserve a short-lived pull permit for the request ID;
- preserve upstream queue-aware weighting among remaining candidates;
- wait only until the earliest permit release within the coordination deadline;
- fall back to upstream selection or recomputation if no permit becomes available.

This filter controls admission to a source; it does not implement KV transfer.

---

## 10. Fan-Out State Model

Sections 10.1-10.6 are implemented only when Z0-B approves distributed placement. A source-pressure-only pivot uses the narrow permit schema in Section 10.7 and does not inherit unused slot-placement state.

### 10.1 Group states

```text
ABSENT
  -> PLANNING
  -> ACTIVE
  -> EXPIRED

ACTIVE
  -> EXPIRED
  -> INVALIDATED
```

`COMPLETE` is optional optimization state, not a correctness dependency. Core cleanup is lease- and TTL-based because a streaming response completion callback must not be assumed unless the pinned llm-d interface reliably exposes one.

### 10.2 Plan record

```go
type FanoutPlan struct {
    GroupID       string
    PrefixKey     string
    Width         uint32
    Epoch         uint64
    CreatedAt     time.Time
    ExpiresAt     time.Time
    PlannerID     string
    Slots         []FanoutSlot
}

type FanoutSlot struct {
    SlotID        uint32
    Target        EndpointRef
    RequestID     string
    ClaimToken    string
    ClaimUntil    time.Time
    State         SlotState
}

type SlotClaim struct {
    GroupID       string
    RequestID     string
    ExpectedEpoch uint64
    LeaseUntil    time.Time
}

type SlotReplan struct {
    GroupID       string
    SlotID        uint32
    RequestID     string
    ExpectedEpoch uint64
    Exclude       []EndpointRef
    LeaseUntil    time.Time
}
```

### 10.3 Slot states

```text
AVAILABLE -> CLAIMED -> DISPATCHED
     |          |
     +----------+-> REPLANNED
```

Claims are leases. A dead EPP cannot permanently own a slot.

`SlotID` is internal. Clients neither choose it nor need to know it.

### 10.4 Valkey key layout

```text
velaserve:v1:group:<group-id>:meta
velaserve:v1:group:<group-id>:slots
velaserve:v1:group:<group-id>:requests
```

All keys for one group share the same hash tag when Valkey Cluster is used so atomic operations remain single-slot.

### 10.5 Atomic operations

The state layer exposes four operations:

```go
type FanoutStore interface {
    CreatePlan(ctx context.Context, plan FanoutPlan) (created bool, current FanoutPlan, err error)
    ClaimSlot(ctx context.Context, claim SlotClaim) (FanoutSlot, error)
    ReplanSlot(ctx context.Context, replan SlotReplan) (FanoutSlot, error)
    GetPlan(ctx context.Context, groupID string) (FanoutPlan, error)
}
```

Lua scripts or equivalent server-side transactions enforce:

- one winning plan epoch;
- one claimant per internal slot;
- one stable slot per `(group, request ID)`;
- retries with the same request ID receive the same claim;
- the number of distinct claimed request IDs cannot exceed declared width;
- stale epochs cannot overwrite newer assignments;
- TTL is set with plan creation and refreshed only within a configured maximum lifetime.

### 10.6 Consistency model

VelaServe requires linearizable coordination only within one fan-out group. It does not require globally consistent endpoint metrics.

Each EPP may observe slightly different queue or KV state while planning. Compare-and-set selects one plan. Every dispatch then validates the selected endpoint against the local current candidate set. A stale plan may be suboptimal, but it must not cause routing to an unhealthy or incompatible endpoint.

### 10.7 Source-pressure permit state

This schema is used only when Z0-C passes:

```go
type PullPermitRequest struct {
    GroupID       string
    RequestID     string
    Candidates    []EndpointRef
    EstimatedBytes uint64
    Deadline      time.Time
}

type PullPermit struct {
    PermitToken   string
    GroupID       string
    RequestID     string
    Source        EndpointRef
    Wave          uint32
    NotBefore     time.Time
    LeaseUntil    time.Time
}

type PullPermitStore interface {
    ReservePull(ctx context.Context, req PullPermitRequest) (PullPermit, error)
    ReleasePull(ctx context.Context, permitToken string) error
}
```

The calibrated `max_concurrent_pulls` value is configuration for each homogeneous source class. A server-side transaction counts live permits, assigns a wave, preserves `RequestID -> PermitToken` idempotency, and sets a maximum lease. Explicit release is an optimization; TTL expiry is the correctness fallback.

```text
velaserve:v1:source:<source-id>:permits
velaserve:v1:group:<group-id>:pulls
```

---

## 11. Planning Algorithm

This section is the Z0-B placement algorithm. It is not implemented when the oracle gate fails.

### 11.1 Objective

For sibling group `G`, minimize the predicted completion time of the slowest sibling:

```text
minimize max(predicted_finish_time(slot_i))
```

The first version uses deterministic greedy list scheduling. It does not use a learned model or bandit.

### 11.2 Inputs

For each eligible endpoint:

- declared group width `N`;
- current token or queue load from upstream producers;
- precise prefix match length;
- calibrated prefill throughput;
- whether an upstream P2P source is available;
- measured pull-versus-recompute threshold;
- predicted endpoint availability after already assigned siblings;
- one frozen sibling service-time estimate, derived from the shared request shape and calibrated `max_tokens` regime;
- optional predicted latency attributes when available.

### 11.3 Estimated slot cost

For sibling `j` and endpoint `e`, the planner estimates:

```text
completion(j, e) =
    max(endpoint_available_time[e], prefix_ready_time[j, e])
  + predicted_service_time[j, e]
```

`prefix_acquisition_cost` is classified as:

1. local cache hit;
2. eligible P2P pull;
3. prefix recomputation.

`endpoint_available_time[e]` contains observed queued work plus siblings already assigned by this plan. It is the reservation mechanism; there is no tunable `group_reservation_penalty`. `prefix_ready_time[j, e]` accounts for local cache availability, calibrated P2P pull, or prefix recomputation.

Version 1 deliberately treats sibling service time as homogeneous because the first request knows `N` but does not carry all future sibling bodies. For the first slot assigned to an endpoint, the planner records when the shared prefix is expected to become ready there. Later slots on that endpoint reuse that readiness estimate instead of charging the prefix acquisition cost repeatedly.

VelaServe must not hard-code one universal transfer rate or silently tune a penalty after seeing benchmark results. Pull, recompute, and service-time models are calibrated for the pinned model, hardware, vLLM build, and network transport. Prediction error is reported through sensitivity analysis against a fixed-service-time fallback.

### 11.4 Assignment

1. Filter unhealthy or incompatible endpoints.
2. Initialize each endpoint's predicted availability from the frozen snapshot.
3. Iterate through stable internal slot order using the frozen homogeneous service-time estimate.
4. Assign the next slot to the endpoint with minimum `completion(j, e)`.
5. Set that endpoint's availability to the predicted completion time of the assigned sibling.
6. Repeat until all `N` slots are assigned.
7. Persist the entire plan atomically.

The number of distinct endpoints selected is the resulting `k`; it is not a manually tuned knob. Reuse and spreading emerge from the same completion-time calculation.

### 11.5 Why reservations matter

Local in-flight accounting lets later requests react to earlier dispatches, but it cannot supply the missing group width `N` or choose the group's replica width `k` once for all siblings. Group reservations expose the work the planner knows will arrive and optimize the maximum predicted completion time rather than a sequence of independent request objectives.

Truly concurrent decisions and cross-EPP dispatches may still amplify the gap, but metric lag is not the primary thesis.

### 11.6 Bounded replanning

If a planned endpoint disappears or becomes ineligible:

- exclude the invalid target;
- recompute only the affected slot;
- increment the plan epoch;
- atomically replace that slot;
- attempt at most one replan on the request path;
- fall back to the upstream profile if replanning fails.

The project does not continuously optimize an active plan.

---

## 12. KV Handling

### 12.1 Core mechanism: planned on-demand pull

The core version does not promise proactive KV prefetch before requests arrive. It plans sibling targets as a group, then uses the existing llm-d P2P mechanism to pull the shared prefix when each sibling begins execution.

This boundary keeps the project implementable without a vLLM engine fork.

### 12.2 Transfer decision

The deployed stack must calibrate `minCachedTokenDelta` or its current upstream equivalent. Pull is requested only when the source holds enough additional cached tokens that transfer is expected to beat recomputation.

### 12.3 Fail-open behavior

If source selection, source reachability, or the P2P connector fails, the model server must recompute normally. A coordination or transfer failure may reduce performance but must not corrupt output or fail an otherwise valid request.

### 12.4 Evidence-gated source-pressure control

Z0-C first determines whether multiple siblings create a pull-serving bottleneck that upstream waiting-queue-aware source selection does not observe. If the gate passes, the first implementation is intentionally small:

1. estimate planned pull service time per source;
2. cap simultaneous planned pulls to one calibrated budget per source;
3. release deferred slots in at most three bounded waves;
4. re-evaluate source eligibility before each wave;
5. fall back to upstream source selection or recomputation on timeout.

The budget is derived from measured source throughput and transfer size, not a hand-tuned score coefficient. Hierarchical `source -> 2 -> 4` propagation remains stretch scope until bounded waves are proven insufficient.

### 12.5 Proactive prefetch stretch

Proactive prefix placement is attempted only if a stable upstream hook can trigger a cache pull without performing the final sibling inference. A synthetic warm-up request is not accepted as production design until it is shown to preserve cache identity, avoid unintended output work, and remain cheaper than on-demand pull.

---

## 13. Request Flows

### 13.1 Ordinary chat request

1. Request contains no valid fan-out metadata.
2. `fanout-profile-handler` selects the upstream profile.
3. No Valkey operation occurs.
4. llm-d performs its normal prefix/load/P2P decision.
5. vLLM streams the response.

### 13.2 First sibling

1. Metadata producer creates `FanoutContext`.
2. Picker reads current endpoint attributes.
3. Picker calculates all `N` slot assignments.
4. `CreatePlan` wins or returns the plan created by another EPP.
5. The request ID is atomically assigned to an internal slot.
6. The assigned endpoint is validated.
7. Existing P2P source selection runs.
8. The request is forwarded and streamed.

### 13.3 Later sibling on another EPP replica

1. The second EPP parses the same group ID.
2. It loads the existing plan from Valkey.
3. It idempotently maps the request ID to one unclaimed internal slot.
4. It validates and uses the assigned target.
5. The request continues through upstream P2P handling.

### 13.4 Duplicate retry

1. The same request ID and group ID arrive again.
2. The store returns the existing claim.
3. No second slot is consumed.
4. Application-level retry semantics remain the client's responsibility; VelaServe guarantees placement idempotency, not response deduplication.

### 13.5 Source-pressure permit, when Z0-C passes

1. Upstream routing chooses the compute target and produces eligible P2P source candidates.
2. `fanout-source-budget-filter` requests a permit using the group and request IDs.
3. The store returns the existing permit for a retry or atomically reserves capacity on an eligible source.
4. The request proceeds immediately or waits until `NotBefore`, bounded by its coordination deadline.
5. If no permit is available in time, the request falls back to upstream source selection or recomputation.
6. A reliable completion hook releases the permit early when available; otherwise its lease expires.

---

## 14. Failure Model

| Failure | Required behavior |
|---|---|
| Valkey timeout or outage | open circuit briefly and route through upstream profile |
| Two EPP replicas create a plan | one CAS winner; loser loads winning plan |
| EPP dies after claim | claim expires; request retry can reclaim or replan |
| Planned endpoint disappears | one-slot replan, then upstream fallback |
| P2P source disappears | upstream pull path fails open to recomputation |
| KV index is warming after restart | bypass group optimization until index readiness gate passes |
| Missing sibling | group and unused slots expire by TTL |
| More distinct request IDs than declared width | route excess requests through upstream profile and increment validation counter |
| Invalid width or group metadata | ignore fan-out metadata and increment validation counter |
| Duplicate request ID | return stable existing slot claim |
| Stale plan epoch | reject mutation and reload current plan |
| Slow Valkey | per-request deadline bounds coordination time; fallback before inference SLO is consumed |
| One source receives excessive planned pulls | apply evidence-gated source budget or fall back to upstream selection/recomputation |

No failure in VelaServe coordination may make an otherwise valid model request unavailable.

---

## 15. Observability

### 15.1 Group-level metrics

VelaServe exposes:

```text
velaserve_plan_groups_total{outcome="planned|fallback|expired|invalidated"}
velaserve_fanout_width
velaserve_plan_duration_seconds
velaserve_plan_fallback_total{reason}
velaserve_slot_replans_total{reason}
velaserve_slot_collisions_total
velaserve_state_operation_seconds{operation,outcome}
velaserve_claim_lease_expired_total
velaserve_orphan_groups_reclaimed_total
velaserve_planned_pulls_total{outcome}
velaserve_source_pull_pressure
velaserve_dispatch_wave_delay_seconds
```

The last three metrics exist only when the Z0-C branch is implemented. Server outcomes describe coordination lifecycle, not inference success.

The benchmark client records:

```text
fanoutbench_group_makespan_seconds
fanoutbench_slowest_child_ttft_seconds
fanoutbench_child_ttft_seconds
fanoutbench_child_request_latency_seconds
fanoutbench_groups_total{outcome="success|failure|cancelled"}
fanoutbench_recomputed_prefix_tokens
fanoutbench_estimated_prefill_gpu_seconds
```

`recomputed_prefix_tokens` is a co-primary result. Prefill GPU-seconds are derived only from a separately calibrated throughput model and are labeled as estimates. Exact FLOPs are not claimed without suitable hardware or operator counters.

### 15.2 Upstream metrics reused

- vLLM waiting/running request metrics;
- prefix cache hit tokens;
- external/offloaded cache hit metrics;
- KV transfer bytes and operations;
- preemption and KV utilization;
- EPP scheduling latency and plugin duration;
- per-endpoint token load and queue depth;
- request TTFT, inter-token latency, and throughput.

### 15.3 Cardinality rules

Group ID, request ID, prompt hash, and endpoint IP must not be Prometheus labels. They may appear as bounded trace attributes or structured logs.

### 15.4 Tracing

One fan-out group is represented by a parent trace with child spans for:

- plan creation or lookup;
- slot claim;
- scheduling;
- queue wait;
- KV acquisition classification;
- inference;
- stream completion.

The benchmark computes makespan from the first sibling dispatch to the last successful sibling completion.

---

## 16. Benchmark Design

### 16.1 Baselines

The baseline definitions, plugin configuration, model revision, and exclusion rules are committed before Z0 begins. Arm B is the primary comparator and cannot be replaced after results are visible.

The primary comparison arms are:

| Arm | Placement | KV behavior | Purpose |
|---|---|---|---|
| A | current llm-d precise affinity | P2P enabled | upstream recommended affinity baseline |
| B | current llm-d load-aware | P2P enabled | strongest existing scatter baseline |
| C | VelaServe N-aware group plan | P2P enabled | proposed placement mechanism, only after Z0-B passes |
| D | current llm-d load-aware | P2P disabled | mechanism control, not résumé headline |
| E | current llm-d load-aware | P2P plus evidence-gated source control | source-pressure branch, only after Z0-C passes |

Round-robin and least-request routing may be included only as educational controls. They are not primary baselines.

### 16.2 Workload

The core workload is one best-of-N group:

- one rendered shared prefix;
- `N` sibling suffixes;
- streaming generation;
- a group succeeds only when all required siblings finish;
- result selection happens after the last sibling, so makespan is user-visible.

This all-of-N completion rule is a conservative upper-bound framing. Real best-of-N systems may early-exit and cancel remaining siblings after obtaining enough candidates. Early exit is excluded from the core comparison so cancellation policy cannot hide scheduling behavior; it may be reported as a separate sensitivity experiment.

The benchmark extends or reuses llm-d benchmark infrastructure rather than recreating vLLM and EPP metric collection.

### 16.3 Factor matrix

At minimum vary:

| Factor | Values |
|---|---|
| fan-out width | 1, 2, 4, 8, 16 |
| prefix length | below, near, and above measured P2P crossover |
| output length | short and moderate decode regimes |
| arrival skew | simultaneous burst and small stagger |
| background load | idle, moderate, and near saturation |
| cache state | warm owner, distributed warm, and cold |
| transport | deployed TCP path; RDMA/EFA only if actually enabled and verified |

Exact token counts are derived from the chosen model context window and the measured transfer crossover. They are not copied blindly from H200 upstream results.

### 16.4 Co-primary results

The first headline plot is:

```text
p95 fan-out makespan vs. fan-out width N
```

It contains separate curves for the applicable Arms A, B, C, and E under the same workload and warmed infrastructure.

The second headline result is:

```text
recomputed prefix tokens per completed group vs. fan-out width N
```

The report may translate avoided recomputation into estimated prefill GPU-seconds using the frozen calibration, but must keep measured tokens and derived GPU time visibly separate.

### 16.5 Secondary results

- p99 slowest-child TTFT;
- sibling target-collision rate;
- successful groups per second;
- P2P bytes and successful pull rounds;
- concurrent pulls and estimated pull service time per source;
- last-sibling TTFT versus source count and fan-out width;
- scheduling and Valkey overhead;
- fairness impact on background ordinary chat traffic;
- benefit heatmap over `(fan-out width, prefix length)`;
- break-even surface over `(prefix length, transport, background load)`.

### 16.6 Experimental discipline

- pin image digests and configuration;
- randomize arm execution order;
- warm peer sessions separately from workload measurement;
- reset cache state consistently between arms;
- use multiple repetitions and report confidence intervals;
- record offered and achieved load;
- count timeouts and failed groups, not only successful latency;
- discard runs with infrastructure restarts unless failure is the experiment;
- preserve raw JSON/Parquet results and exact manifests;
- never change the baseline after seeing the result.
- preserve negative and failed runs rather than filtering them from the report;
- distinguish measured counters from model-derived estimates.

### 16.7 Valid negative result

If VelaServe does not improve makespan or source-pressure behavior, the report must still answer:

- whether current load-aware + P2P already distributes siblings adequately;
- how much Valkey and group planning add to request latency;
- whether P2P transfer or decode dominates the group;
- at what widths or loads collisions actually occur;
- whether proactive predeclaration would be necessary to create value.
- whether the N-aware oracle itself had negligible regret, proving coordination was unnecessary;
- whether source pull pressure appeared before, at, or after the fleet-width crossover.

---

## 17. AWS Deployment

### 17.1 EKS topology

- one EKS cluster;
- one CPU system node pool;
- one homogeneous GPU NodePool managed by Karpenter;
- eight vLLM replicas for the headline result, with six as the minimum valid fleet width;
- one GPU per vLLM replica for the initial experiment;
- two EPP replicas;
- Valkey with a primary and recoverable replica or a managed equivalent, only for a gate-approved branch;
- Prometheus, Grafana, and OpenTelemetry Collector;
- Gateway API-compatible Envoy data plane.

### 17.2 Initial GPU shape

The default practical target is one L40S GPU per node using a G6e single-GPU instance shape. The exact shape and region remain quota-dependent and must be pinned in the benchmark manifest.

An open 8B- to 14B-class instruction model with a sufficiently long context window is used so one replica fits on one L40S. A smaller model may be used for simulator and wiring tests, but not for the headline P2P-versus-recompute crossover. Model name, tokenizer, chat template, quantization, and revision are pinned.

### 17.3 Network reality

G6e experiments should assume the verified transport actually available to the Pods. If NIXL/UCX falls back to TCP, the benchmark reports TCP results and calibrates the pull threshold accordingly.

H100/H200 and EFA/RDMA validation is optional. Results from a different transport must not be mixed into one curve.

### 17.4 Karpenter scope

Karpenter is used to provision the homogeneous GPU pool and demonstrate replacement of one failed GPU node. It is not used to build a heterogeneous cost optimizer.

### 17.5 Infrastructure as code

Terraform owns:

- VPC and subnets;
- EKS cluster and IAM roles;
- Karpenter prerequisites;
- ECR repositories;
- S3 benchmark artifact bucket;
- security groups;
- optional managed Valkey.

Helm or Kustomize owns Kubernetes applications and experiment configurations.

---

## 18. Local Development Environment

Local development does not attempt to reproduce GPU performance.

It runs:

- Kind;
- one- and two-replica EPP configurations;
- llm-d inference simulator endpoints;
- the Z0 placement recorder and N-aware oracle replay tool;
- Valkey only after a coordination branch passes its gate;
- Envoy / Gateway API;
- `fanoutbench` with deterministic simulated endpoint attributes.

Local goals:

- reproduce upstream load-aware and affinity baselines;
- measure the scheduling-to-in-flight publication window;
- quantify placement dispersion and oracle regret;
- validate plugin wiring;
- test plan atomicity across EPP replicas;
- inject endpoint churn;
- run state-machine and property tests;
- verify fallback and streaming protocol behavior.

All performance conclusions come from real GPU runs.

---

## 19. Testing Strategy

### 19.1 Unit tests

- metadata validation;
- baseline result-schema validation;
- oracle completion-time calculation;
- deterministic plan generation;
- cost classification;
- plan serialization;
- Valkey error mapping;
- fallback selection;
- bounded replanning.

### 19.2 Property and invariant tests

The implementation must establish:

1. An assigned endpoint is always in the eligible candidate set at dispatch.
2. Each internal slot has at most one distinct request-ID claimant.
3. Repeating the same request ID returns the same internal slot claim.
4. An older epoch cannot overwrite a newer plan.
5. Every group key expires within the configured maximum lifetime.
6. Invalid metadata never enters the group scheduler.
7. Valkey failure cannot fail an otherwise valid inference request.
8. Ordinary traffic does not perform fan-out state operations.
9. A one-slot replan cannot change unaffected sibling assignments.
10. The planner is deterministic for the same candidate snapshot and configuration.
11. The planner's selected `k` is a consequence of frozen completion estimates, not a tunable spreading constant.
12. The oracle never reports a worse predicted maximum completion time than the same frozen model applied to Arm B.

### 19.3 Integration tests

- concurrent first siblings arriving at different EPP replicas;
- duplicate request ID and more request IDs than declared width;
- EPP restart after plan creation;
- assigned endpoint deletion;
- delayed or incomplete KV events;
- Valkey latency and outage;
- claim lease expiration;
- group TTL reclamation;
- P2P unavailable with successful recompute fallback;
- ordinary SSE response through the upstream profile.

### 19.4 Chaos tests

1. Terminate one GPU node during active groups.
2. Block Valkey for a bounded interval.
3. Restart one EPP replica while the other continues.
4. Make one planned vLLM endpoint fail readiness.

The expected outcome is reduced optimization quality or slot replanning, not request corruption.

### 19.5 Performance tests

- planner CPU time as fleet size and width grow;
- Valkey p50/p95/p99 operation latency;
- added EPP scheduling latency;
- ordinary path regression;
- group makespan and slowest-child TTFT;
- state-store load under many simultaneous groups.
- placement and source-pressure gate reproducibility;
- prediction sensitivity when output-length estimates are inaccurate;
- source-budget throughput and last-sibling TTFT when the Z0-C branch is active.

---

## 20. Repository Structure

```text
velaserve/
├── cmd/
│   ├── fanoutbench/
│   └── zeroprobe/
├── research/
│   ├── preregistration/
│   ├── placement-recorder/
│   ├── oracle-replay/
│   └── gate-decision/
├── internal/
│   ├── fanout/
│   │   ├── model/
│   │   ├── planner/
│   │   ├── state/
│   │   └── protocol/
│   ├── metrics/
│   └── trace/
├── pkg/
│   └── fanoutstore/
│       ├── memory/
│       └── valkey/
├── llm-d-router-patch/
│   ├── metadata-producer/
│   ├── profile-handler/
│   ├── fanout-picker/
│   └── registration.patch
├── deploy/
│   ├── helm/
│   ├── gateway/
│   ├── observability/
│   └── experiments/
├── infra/
│   └── terraform/
├── benchmarks/
│   ├── profiles/
│   ├── analysis/
│   ├── raw/
│   └── expected-schema/
├── tests/
│   ├── integration/
│   ├── property/
│   └── chaos/
└── docs/
    ├── architecture.md
    ├── state-consistency.md
    ├── benchmark-methodology.md
    ├── failure-analysis.md
    └── performance-report.md
```

The llm-d Router modification must remain a small, reviewable patch. Core planner and state code live in independent Go packages with no dependency on the full EPP runtime.

---

## 21. Upstream Integration Strategy

1. Pin llm-d Router, llm-d, llm-d-kv-cache, vLLM, and connector image revisions.
2. Run Z0-A/B/C and commit the signed gate decision before coordination code exists.
3. Confirm the chosen contribution does not duplicate a newer upstream implementation.
4. Implement only the gate-approved planner or source-pressure mechanism outside the upstream fork first.
5. Add the smallest possible EPP registration patch.
6. Keep the fork rebaseable and publish the exact upstream commit.
7. Open an llm-d design issue with the benchmark question before proposing a large PR.
8. Submit an upstream PR only after the mechanism and evidence are stable.

A merged PR is a high-value bonus, not a completion criterion.

---

## 22. Definition of Done

### 22.1 Required research completion

VelaServe first satisfies all of the following:

1. The upstream revisions, Arm A/B definitions, workloads, exclusion rules, and 10% gate threshold are frozen before results are viewed.
2. Z0-A records actual placement for `N in {2, 4, 8, 16}` with one and two EPP replicas.
3. Z0-B compares Arm B against the N-aware oracle using the same frozen endpoint snapshots.
4. Z0-C measures source selection and last-sibling behavior as prefix-source count and `N` vary.
5. A signed gate decision selects placement, source-pressure control, or termination without changing the baseline.
6. Raw zeroing data, analysis code, manifests, and negative runs are preserved.

If both gates fail, these artifacts plus a reproducible negative-result report complete the research project; no unused Valkey or EPP coordination scaffold is added.

### 22.2 Placement-branch completion

If Z0-B passes, all of the following are additionally required:

1. Two active EPP replicas coordinate one fan-out plan without conflicting slot claims.
2. Valid declared fan-out requests map stable request IDs to internal slots.
3. Requests without fan-out metadata use the pinned upstream profile and bypass Valkey.
4. The planner consumes real upstream endpoint and prefix-cache attributes.
5. Endpoint loss triggers bounded replan or upstream fallback.
6. Valkey outage does not make inference unavailable.
7. Group keys and orphan slots are reclaimed within a bounded TTL.
8. Unit, property, integration, and chaos suites pass.

### 22.3 Source-pressure-branch completion

If Z0-C passes, all of the following are additionally required:

1. Existing upstream P2P source selection remains the comparison baseline.
2. Planned pull pressure is bounded using measured source capacity rather than a scoring constant.
3. Deferred waves are bounded and respect the request deadline.
4. Source-control failure falls back to upstream selection or recomputation.
5. Concurrent pulls, source pressure, and last-sibling TTFT are published.

### 22.4 Final evidence completion

Every implemented branch must satisfy:

1. The headline stack runs on AWS EKS with eight real vLLM replicas, with six as the minimum acceptable fallback.
2. Applicable arms are benchmarked under identical pinned conditions.
3. Results include confidence intervals, failure counts, and raw artifacts.
4. `makespan vs. N` and `recomputed prefix tokens vs. N` are published.
5. A break-even map and, when applicable, a source-pressure plot are published.
6. Ordinary chat performance regression is measured and disclosed.
7. The report states whether each hypothesis held and where it failed.
8. The repository includes reproducible deployment and cleanup instructions.

No QLoRA run, web console, multi-cloud deployment, or upstream merge is required.

---

## 23. Portfolio Deliverables

The public project should contain:

- concise README with the one-sentence thesis;
- pre-registered zeroing protocol and signed gate decision;
- placement traces and N-aware oracle replay code;
- architecture and state-consistency documents;
- Go API documentation for the planner and store;
- exact upstream version matrix;
- Terraform and Kubernetes manifests;
- benchmark methodology before performance results;
- raw benchmark artifacts;
- Grafana dashboard export;
- failure-injection evidence;
- performance report with positive and negative regions;
- short demo video showing one fan-out trace across multiple replicas;
- optional llm-d issue or PR link.

The repository must lead with the measured systems result, not the number of installed technologies.

---

## 24. Interview Narrative

The interview story is:

1. Current vLLM/llm-d already handles request-level cache and load routing well.
2. Agent fan-out introduces a group objective: slowest-child completion time.
3. Local in-flight accounting does not give a per-request scheduler the group width `N` or let it choose the replica width `k` jointly.
4. Before implementation, a zeroing experiment measures whether an N-aware oracle has material regret over the strongest upstream baseline.
5. Only after that gate does VelaServe create one atomic group plan across active-active EPP replicas.
6. Existing llm-d P2P transfer is reused rather than reimplemented; source-pressure control is separately evidence-gated.
7. Valkey, when justified, provides narrow and expiring coordination with leases and fencing.
8. Every optimization failure falls back to upstream inference.
9. The contribution is demonstrated with `makespan vs. N`, recomputed-prefix work, and a break-even surface rather than an assumed positive result.

### Résumé bullet templates

Use measured values only after the final benchmark:

- Built a Go extension to llm-d Router for declared best-of-N fan-out, atomically coordinating sibling placement across active-active EPP replicas and reusing vLLM peer-to-peer KV transfer.
- Designed a lease- and epoch-based Valkey state machine with idempotent slot claims, bounded replanning, orphan cleanup, and fail-open fallback to the upstream token-aware routing path.
- Built a pre-registered Go replay oracle that evaluated every feasible fan-out replica width against llm-d `load-aware + P2P` before authorizing distributed coordination work.
- Benchmarked fan-out scheduling on AWS EKS across six to eight vLLM GPU replicas, publishing p95/p99 group makespan, recomputed prefix tokens, slowest-child TTFT, KV-transfer evidence, and the measured pull/coordination break-even region.

Do not insert improvement percentages until reproduced results exist.

---

## 25. Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Current load-aware + P2P already chooses a near-optimal `k` | stop the placement branch at Z0-B and publish the oracle-regret result |
| Local in-flight behavior changes upstream | pin the exact commit and rerun Z0 before implementation |
| A production cross-EPP syncer appears upstream | compare it as a new baseline; do not claim cross-replica visibility as novelty |
| P2P over TCP is slower than recomputation | calibrate crossover; restrict pulls; report transport-specific boundary |
| Queue-aware P2P source sampling already prevents herd behavior | stop the source-pressure branch at Z0-C |
| Group pull waves overload one source or delay too long | derive a source budget from throughput; bound waves; fail open |
| EPP plugin interfaces change | pin versions, keep patch small, isolate core packages |
| Valkey adds too much request latency | ordinary path bypass; strict timeout; pipelined/Lua operations; fallback |
| Fan-out metadata is not adopted by real clients | keep contract minimal; provide client middleware and benchmark adapter |
| Plan is stale before siblings arrive | short TTL, endpoint validation, one-slot replan |
| Exact response completion hook is unavailable | use leases and TTL as correctness mechanism |
| Synthetic benchmark does not resemble agents | add one deterministic MCP/subagent demo after the controlled benchmark |
| AWS GPU quota blocks the preferred fleet | move region or instance shape; do not publish the headline fleet-width curve below six isolated replicas |
| Improvement appears only in a narrow corner | publish the break-even map; do not overgeneralize |

---

## 26. Rejected Alternatives

### 26.1 Build a parallel Go inference router

Rejected because ext-proc handling, endpoint discovery, health tracking, prefix indexing, and standard request-level scorers already exist in llm-d Router.

### 26.2 Build an independent fan-out coordinator service

Rejected for the core version because it introduces a new request hop and control-plane API. Fan-out coordination fits inside the EPP plugin path plus a narrow shared store.

### 26.3 Keep all fan-out state in EPP memory

Rejected because two active EPP replicas could create conflicting plans and lose group state on restart.

### 26.4 Make heterogeneous cost routing the thesis

Rejected because current llm-d already contains latency-aware routing and heterogeneous/cost-aware autoscaling capabilities. It also weakens the direct fan-out hypothesis.

### 26.5 Implement full program-aware scheduling

Rejected because sequential loops, symmetric fan-out, and arbitrary DAG critical paths have different objectives. One scorer covering all three would be difficult to validate and explain.

### 26.6 Require proactive KV pre-replication

Rejected as a core requirement because the stable upstream trigger is not yet established. Planned on-demand P2P pull is implementable with the existing serving path.

### 26.7 Make hierarchical KV propagation core scope

Rejected before Z0-C because upstream source selection already accounts for inference waiting queues, and a source-pressure-aware budget may be sufficient. A broadcast tree adds coordination and failure modes and is implemented only after evidence shows bounded dispatch waves cannot remove the bottleneck.

---

## 27. Official Technical References

- llm-d Router: https://github.com/llm-d/llm-d-router
- llm-d agentic-serving guide: https://github.com/llm-d/llm-d/tree/main/guides/agentic-serving
- llm-d agentic workload direction: https://github.com/llm-d/llm-d/blob/main/docs/well-lit-paths/workloads/agentic-serving.md
- llm-d P2P KV sharing guide: https://github.com/llm-d/llm-d/tree/main/guides/p2p-kv-cache-sharing
- llm-d precise prefix-cache routing: https://github.com/llm-d/llm-d/tree/main/guides/precise-prefix-cache-routing
- llm-d Router scheduling model: https://github.com/llm-d/llm-d/blob/main/docs/architecture/core/router/epp/scheduling.md
- llm-d in-flight load producer: https://github.com/llm-d/llm-d-router/blob/main/pkg/epp/framework/plugins/requestcontrol/dataproducer/inflightload/producer.go
- llm-d concurrent scheduling/in-flight issue: https://github.com/llm-d/llm-d-router/issues/2295
- llm-d P2P source producer: https://github.com/llm-d/llm-d-router/blob/main/pkg/epp/framework/plugins/requestcontrol/dataproducer/p2psource/producer.go
- llm-d P2P source-pressure design issue: https://github.com/llm-d/llm-d-router/issues/2273
- llm-d current local-only syncer: https://github.com/llm-d/llm-d-router/blob/main/pkg/epp/framework/plugins/datalayer/cross_plugin/cross_replica_syncer_mock.go
- llm-d KV-cache index architecture: https://github.com/llm-d/llm-d-kv-cache/blob/main/docs/architecture.md
- vLLM production metrics: https://docs.vllm.ai/en/latest/usage/metrics/
- vLLM KV events: https://docs.vllm.ai/en/latest/api/vllm/config/kv_events/
- AWS EC2 G6e: https://aws.amazon.com/ec2/instance-types/g6e/
- AWS accelerated instance specifications: https://docs.aws.amazon.com/ec2/latest/instancetypes/ac.html

---

## 28. Final Positioning

> **VelaServe — Fan-out-aware scheduling for agentic LLM serving.**  
> Tests whether explicit group width enables better fan-out replica-width decisions than per-request llm-d routing, then implements only the evidence-supported N-aware placement or P2P source-pressure mechanism.

This is ML serving and runtime infrastructure, not a general MLOps platform. Its breadth comes from solving one falsifiable systems problem end to end across Go, distributed coordination, Kubernetes, Envoy, vLLM, GPU cache behavior, observability, reliability, and controlled experimentation—not from assembling unrelated platform features.
