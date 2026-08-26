# VelaServe Stage 1 Zeroing Toolchain Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build and publish the complete AWS-independent VelaServe research, replay, benchmark, local-integration, and cloud-handoff toolchain while enforcing the v3 rule that production coordination code cannot exist before a signed real-GPU gate decision.

**Architecture:** A Go module owns versioned evidence records, deterministic baseline policies, an exhaustive N-aware replay oracle, bootstrap confidence intervals, signed gate decisions, and OpenAI-compatible streaming workload generation. Commands consume and emit append-only JSONL so simulator, Kind, and later AWS runs share the same contracts. Deployment assets pin upstream revisions and render locally, but the placement and source-pressure production adapters remain absent until a verified gate authorizes exactly one follow-up plan.

**Tech Stack:** Go 1.26.6, standard-library HTTP/SSE/JSON/crypto packages, Cobra, Prometheus client, OpenTelemetry, Testify, Rapid property tests, JSON Schema 2020-12, Make, Docker, Kind, Helm, Kustomize, Terraform, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-08-25-velaserve-design.md`

## Global Constraints

- Primary language is Go; module path is `github.com/JDinSeattle/velaserve`.
- Freeze llm-d Router at `ab723b898f8598ab6631e9848a4cf28accd9b9ea`.
- Freeze llm-d at `3243fcf1191348b55c7811267a98117f8b7a6910`.
- Freeze llm-d-kv-cache at `8cf43067afb7fc9fefafc1b64de063c769f2c90f`.
- Freeze vLLM at `b1fbbc2ade51e3826bc92e4733c9c692ee21d42d`.
- Arm B (`load-aware + P2P`) is the immutable primary comparator.
- The practical-significance threshold is `0.10`; placement requires the lower 95% confidence bound to meet it at two adjacent fan-out widths.
- Fan-out widths for Z0 are exactly `2, 4, 8, 16`; arrival skews are exactly `0ms, 1ms, 5ms, 20ms`.
- Ordinary requests and invalid fan-out metadata never use coordination state.
- Group IDs, request IDs, prompt hashes, and endpoint IPs are forbidden Prometheus labels.
- Raw and failed runs are append-only evidence; analysis never deletes or silently filters them.
- Measured counters and derived estimates remain separate fields.
- No Valkey package, EPP fan-out picker, or source-budget filter is implemented in this plan.
- AWS resources are rendered and statically validated only; no cloud API is called by this plan.

---

### Task 1: Repository Contract, Upstream Lock, and Evidence Schemas

**Files:**
- Create: `go.mod`
- Create: `Makefile`
- Create: `.gitignore`
- Create: `.github/workflows/ci.yml`
- Create: `versions.lock.yaml`
- Create: `internal/evidence/model.go`
- Create: `internal/evidence/validate.go`
- Create: `internal/evidence/validate_test.go`
- Create: `benchmarks/expected-schema/placement-event.schema.json`
- Create: `benchmarks/expected-schema/group-result.schema.json`
- Create: `benchmarks/expected-schema/gate-decision.schema.json`
- Create: `research/preregistration/z0-v1.yaml`

**Interfaces:**
- Consumes: the v3 specification and frozen upstream SHAs in Global Constraints.
- Produces: `evidence.PlacementEvent`, `evidence.GroupResult`, `evidence.GateDecision`, `evidence.ValidatePlacementEvent`, `evidence.ValidateGroupResult`, and immutable preregistration data used by every later task.

- [ ] **Step 1: Write failing schema and validation tests**

```go
func TestValidatePlacementEventRejectsUnregisteredWidth(t *testing.T) {
	e := validPlacementEvent()
	e.FanoutWidth = 3
	require.ErrorContains(t, evidence.ValidatePlacementEvent(e), "fanout_width")
}

func TestValidateGroupResultKeepsFailedRun(t *testing.T) {
	r := validGroupResult()
	r.Outcome = evidence.OutcomeFailure
	r.Failure = "upstream timeout"
	require.NoError(t, evidence.ValidateGroupResult(r))
}
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run: `go test ./internal/evidence -run 'TestValidate' -count=1`

Expected: FAIL because package `internal/evidence` does not exist.

- [ ] **Step 3: Define the versioned evidence records and strict validation**

```go
type PlacementEvent struct {
	SchemaVersion string            `json:"schema_version"`
	RunID         string            `json:"run_id"`
	Arm           Arm               `json:"arm"`
	GroupID       string            `json:"group_id"`
	RequestID     string            `json:"request_id"`
	FanoutWidth   uint32            `json:"fanout_width"`
	ArrivalSkewMS uint32            `json:"arrival_skew_ms"`
	EPPReplicas   uint32            `json:"epp_replicas"`
	LoadRegime    LoadRegime        `json:"load_regime"`
	Snapshot      EndpointSnapshot  `json:"snapshot"`
	Target        EndpointRef       `json:"target"`
	ObservedAt    time.Time         `json:"observed_at"`
	Attributes    map[string]string `json:"attributes,omitempty"`
}

type GroupResult struct {
	SchemaVersion           string        `json:"schema_version"`
	RunID                   string        `json:"run_id"`
	Arm                     Arm           `json:"arm"`
	GroupID                 string        `json:"group_id"`
	FanoutWidth             uint32        `json:"fanout_width"`
	MakespanSeconds         float64       `json:"makespan_seconds"`
	SlowestChildTTFTSeconds float64       `json:"slowest_child_ttft_seconds"`
	RecomputedPrefixTokens  uint64        `json:"recomputed_prefix_tokens"`
	EstimatedPrefillSeconds *float64      `json:"estimated_prefill_gpu_seconds,omitempty"`
	Outcome                 Outcome       `json:"outcome"`
	Failure                 string        `json:"failure,omitempty"`
	Children                []ChildResult `json:"children"`
}
```

Validation accepts unsuccessful results when a failure reason is present, rejects NaN/Inf/negative timings, and permits only frozen widths/skews/arms.

- [ ] **Step 4: Add machine-readable JSON Schemas and preregistration**

`research/preregistration/z0-v1.yaml` must contain the exact arms, factor matrix, seed `20260825`, bootstrap repetitions `10000`, confidence level `0.95`, threshold `0.10`, adjacency order `[2,4,8,16]`, exclusion rules, and all four upstream SHAs. JSON Schemas set `additionalProperties: false` for stable evidence records.

- [ ] **Step 5: Run tests and schema checks and verify GREEN**

Run: `go test ./internal/evidence -count=1`

Expected: PASS.

Run: `go run ./cmd/schema-check --schema-dir benchmarks/expected-schema --fixture-dir internal/evidence/testdata`

Expected: PASS after adding valid and invalid fixtures alongside the checker in Task 2.

- [ ] **Step 6: Commit the contract**

```bash
git add go.mod Makefile .gitignore .github versions.lock.yaml internal/evidence benchmarks/expected-schema research/preregistration
git commit -m "feat: freeze VelaServe experiment contract"
```

### Task 2: JSONL I/O, Schema Checker, and Append-Only Artifact Ledger

**Files:**
- Create: `internal/jsonl/jsonl.go`
- Create: `internal/jsonl/jsonl_test.go`
- Create: `internal/evidence/testdata/placement-valid.json`
- Create: `internal/evidence/testdata/placement-invalid.json`
- Create: `internal/evidence/testdata/group-valid.json`
- Create: `cmd/schema-check/main.go`
- Create: `internal/artifacts/ledger.go`
- Create: `internal/artifacts/ledger_test.go`

**Interfaces:**
- Consumes: Task 1 evidence types and schemas.
- Produces: `jsonl.Read[T]`, `jsonl.Append[T]`, `artifacts.Record`, `artifacts.Verify`, and `schema-check`.

- [ ] **Step 1: Write failing round-trip and tamper-detection tests**

```go
func TestAppendPreservesExistingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	require.NoError(t, jsonl.Append(path, first))
	require.NoError(t, jsonl.Append(path, second))
	got, err := jsonl.Read[evidence.PlacementEvent](path)
	require.NoError(t, err)
	require.Equal(t, []evidence.PlacementEvent{first, second}, got)
}

func TestVerifyDetectsChangedArtifact(t *testing.T) {
	ledger := createLedgerWithOneArtifact(t)
	require.NoError(t, os.WriteFile(ledger.Path, []byte("changed"), 0o600))
	require.ErrorContains(t, artifacts.Verify(ledger.Root), "sha256 mismatch")
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/jsonl ./internal/artifacts -count=1`

Expected: FAIL because both packages are missing.

- [ ] **Step 3: Implement generic JSONL and SHA-256 ledger operations**

```go
func Append[T any](path string, value T) error
func Read[T any](path string) ([]T, error)

type Entry struct {
	RelativePath string    `json:"relative_path"`
	SHA256       string    `json:"sha256"`
	Bytes        int64     `json:"bytes"`
	RecordedAt   time.Time `json:"recorded_at"`
}

func Record(root, relativePath string) (Entry, error)
func Verify(root string) error
```

`Append` opens with `O_APPEND|O_CREATE|O_WRONLY`, emits exactly one JSON object plus newline, calls `Sync`, and never rewrites earlier records. `Record` rejects paths outside the artifact root.

- [ ] **Step 4: Implement `schema-check` over every fixture**

The command loads all three schemas, validates `*-valid.json`, requires `*-invalid.json` to fail, and exits non-zero when a schema or fixture is missing.

- [ ] **Step 5: Verify GREEN**

Run: `go test ./internal/jsonl ./internal/artifacts -count=1`

Expected: PASS.

Run: `go run ./cmd/schema-check --schema-dir benchmarks/expected-schema --fixture-dir internal/evidence/testdata`

Expected: `schema-check: 3 valid fixtures accepted; 1 invalid fixture rejected`.

- [ ] **Step 6: Commit evidence I/O**

```bash
git add internal/jsonl internal/artifacts internal/evidence/testdata cmd/schema-check
git commit -m "feat: add append-only experiment artifact ledger"
```

### Task 3: Frozen Cost Model and Deterministic N-Aware Planner

**Files:**
- Create: `internal/fanout/model/types.go`
- Create: `internal/fanout/planner/planner.go`
- Create: `internal/fanout/planner/planner_test.go`
- Create: `tests/property/planner_test.go`

**Interfaces:**
- Consumes: `evidence.EndpointSnapshot`, declared width, and one frozen `planner.Calibration`.
- Produces: `planner.Plan(ctx, Input) (Plan, error)` where `Plan` contains ordered slot assignments, predicted finishes, acquisition class, and derived replica width `K`.

- [ ] **Step 1: Write failing deterministic and eligibility tests**

```go
func TestPlanSpreadsWhenWarmOwnerIsBusy(t *testing.T) {
	in := fixtureInput(4, []EndpointState{
		{Ref: ep("warm"), AvailableAt: 12, LocalPrefixTokens: 4096},
		{Ref: ep("idle"), AvailableAt: 0, P2PSource: ep("warm")},
	})
	got, err := planner.Plan(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, uint32(2), got.K)
	require.Equal(t, []EndpointRef{ep("idle"), ep("idle"), ep("warm"), ep("idle")}, targets(got))
}

func TestPlanNeverUsesIneligibleEndpoint(t *testing.T) {
	in := fixtureInput(8, []EndpointState{{Ref: ep("dead"), Healthy: false}, {Ref: ep("live"), Healthy: true}})
	got, err := planner.Plan(context.Background(), in)
	require.NoError(t, err)
	require.ElementsMatch(t, []EndpointRef{ep("live")}, distinctTargets(got))
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/fanout/planner -count=1`

Expected: FAIL because planner symbols are undefined.

- [ ] **Step 3: Implement immutable inputs and completion-time list scheduling**

```go
type Calibration struct {
	PrefillTokensPerSecond float64 `json:"prefill_tokens_per_second"`
	PullBytesPerSecond     float64 `json:"pull_bytes_per_second"`
	BytesPerCachedToken    float64 `json:"bytes_per_cached_token"`
	ServiceSeconds         float64 `json:"service_seconds"`
}

type SlotAssignment struct {
	SlotID          uint32           `json:"slot_id"`
	Target          model.EndpointRef `json:"target"`
	Acquisition     AcquisitionClass `json:"acquisition"`
	PrefixReadyAt   float64          `json:"prefix_ready_at_seconds"`
	PredictedFinish float64          `json:"predicted_finish_seconds"`
}

func Plan(ctx context.Context, in Input) (Plan, error)
```

Tie-breaking is endpoint stable ID then slot ID. The first assignment to an endpoint pays local-hit, P2P, or recompute cost; later assignments reuse that endpoint's recorded prefix-ready time. No spreading penalty exists.

- [ ] **Step 4: Add Rapid properties**

Generate 1-32 endpoints and widths 1-64. Assert the same input serializes to the same plan, every target is healthy and compatible, slot IDs are contiguous, `K` equals the number of distinct targets, and predicted finish times are finite and nondecreasing per endpoint.

- [ ] **Step 5: Verify GREEN and race safety**

Run: `go test ./internal/fanout/planner ./tests/property -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit planner**

```bash
git add internal/fanout tests/property
git commit -m "feat: add deterministic fan-out completion planner"
```

### Task 4: Arm A/B Replay Policies and Exhaustive Replica-Width Oracle

**Files:**
- Create: `research/oracle-replay/policy.go`
- Create: `research/oracle-replay/policy_test.go`
- Create: `research/oracle-replay/oracle.go`
- Create: `research/oracle-replay/oracle_test.go`
- Create: `cmd/oracle-replay/main.go`

**Interfaces:**
- Consumes: Task 1 placement snapshots and Task 3 cost model.
- Produces: `replay.ReplayArmA`, `replay.ReplayArmB`, `replay.EvaluateAllWidths`, and JSONL `OracleResult` records containing baseline prediction, best `k`, oracle prediction, and regret.

- [ ] **Step 1: Write failing baseline and oracle-bound tests**

```go
func TestOracleEvaluatesEveryFeasibleK(t *testing.T) {
	result, err := replay.EvaluateAllWidths(fixtureSnapshot(4), 8, calibration())
	require.NoError(t, err)
	require.Equal(t, []uint32{1, 2, 3, 4}, evaluatedKs(result))
}

func TestOracleNeverWorseThanFrozenArmBModel(t *testing.T) {
	snapshot := fixtureSnapshot(8)
	armB := replay.ReplayArmB(snapshot, 16, calibration())
	oracle, err := replay.EvaluateAllWidths(snapshot, 16, calibration())
	require.NoError(t, err)
	require.LessOrEqual(t, oracle.BestMakespan, armB.PredictedMakespan)
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./research/oracle-replay -count=1`

Expected: FAIL because replay policies do not exist.

- [ ] **Step 3: Implement the immutable upstream comparators**

Arm A selects the longest precise prefix match subject to the frozen upstream load gate and then uses stable load/ID tie-breaking. Arm B schedules one sibling at a time using the frozen load-aware score plus local in-flight publication timing from each event; P2P changes prefix-acquisition cost but does not make an N-wide plan.

```go
type Policy interface {
	Name() evidence.Arm
	Replay(snapshot evidence.EndpointSnapshot, width uint32, c planner.Calibration) (ReplayResult, error)
}
```

- [ ] **Step 4: Enumerate every `k` and candidate subset**

For each `k` in `[1,min(N,healthy)]`, enumerate stable endpoint combinations, run the same frozen completion model constrained to that subset, keep the minimum maximum finish, and tie-break lexicographically. Include the unconstrained Arm B assignment as a candidate so the oracle-bound invariant is structural.

- [ ] **Step 5: Implement the CLI**

Run form:

```bash
go run ./cmd/oracle-replay \
  --placements benchmarks/raw/z0/placements.jsonl \
  --calibration benchmarks/profiles/local-sim.yaml \
  --output benchmarks/raw/z0/oracle.jsonl
```

The command refuses an existing output unless `--append` is passed and records input SHA-256 values in every result.

- [ ] **Step 6: Verify GREEN**

Run: `go test ./research/oracle-replay -race -count=1`

Expected: PASS.

- [ ] **Step 7: Commit replay and oracle**

```bash
git add research/oracle-replay cmd/oracle-replay
git commit -m "feat: add frozen upstream replay and N-aware oracle"
```

### Task 5: Deterministic Statistics and Gate Decision Engine

**Files:**
- Create: `benchmarks/analysis/bootstrap.go`
- Create: `benchmarks/analysis/bootstrap_test.go`
- Create: `research/gate-decision/decision.go`
- Create: `research/gate-decision/decision_test.go`
- Create: `research/gate-decision/sign.go`
- Create: `research/gate-decision/sign_test.go`
- Create: `cmd/velaserve-gate/main.go`

**Interfaces:**
- Consumes: `GroupResult`, `OracleResult`, preregistration seed/repetitions/threshold, and optional Ed25519 key material.
- Produces: deterministic confidence intervals and a signed `GateDecision` of `placement`, `source-pressure`, or `negative-result`.

- [ ] **Step 1: Write failing adjacency and signature tests**

```go
func TestPlacementNeedsTwoAdjacentPassingWidths(t *testing.T) {
	input := resultsWithLowerBounds(map[uint32]float64{2: .11, 4: .12, 8: .09, 16: .13})
	got, err := gate.Decide(input, prereg())
	require.NoError(t, err)
	require.Equal(t, evidence.BranchPlacement, got.Branch)
	require.Equal(t, []uint32{2, 4}, got.QualifyingWidths)
}

func TestTamperedDecisionFailsVerification(t *testing.T) {
	priv, pub := deterministicTestKey()
	signed := gate.MustSign(validDecision(), priv)
	signed.Decision.Threshold = .09
	require.Error(t, gate.Verify(signed, pub))
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./benchmarks/analysis ./research/gate-decision -count=1`

Expected: FAIL because analysis and gate packages are missing.

- [ ] **Step 3: Implement seeded paired bootstrap confidence intervals**

```go
type Interval struct { Estimate, Lower, Upper float64 }
func PairedImprovementCI(baseline, candidate []float64, seed int64, repetitions int, confidence float64) (Interval, error)
```

Pair by run repetition and workload cell. Reject unequal lengths, fewer than 20 pairs, non-finite values, or baseline values `<= 0`. Sampling is deterministic for the preregistered seed.

- [ ] **Step 4: Implement the immutable decision tree**

Placement wins only with two adjacent widths whose lower p95 makespan improvement bound is at least `0.10` in one identical load/prefix/transport regime. If placement fails, source pressure wins only when the lower last-sibling TTFT improvement bound is at least `0.10`; otherwise select negative result. Missing cells yield `insufficient-evidence`, which cannot be signed as a final gate.

- [ ] **Step 5: Implement canonical JSON Ed25519 signing and verification**

```go
func Sign(decision evidence.GateDecision, private ed25519.PrivateKey) (SignedDecision, error)
func Verify(signed SignedDecision, public ed25519.PublicKey) error
```

Canonical bytes are produced from a struct with fixed field order; signatures never cover map iteration. Private keys are never created inside the repository and `*.private.key` is ignored.

- [ ] **Step 6: Verify GREEN**

Run: `go test ./benchmarks/analysis ./research/gate-decision -race -count=1`

Expected: PASS.

- [ ] **Step 7: Commit gate analysis**

```bash
git add benchmarks/analysis research/gate-decision cmd/velaserve-gate
git commit -m "feat: add preregistered gate analysis and signing"
```

### Task 6: Fan-Out Metadata and OpenAI SSE Benchmark Client

**Files:**
- Create: `internal/fanout/protocol/headers.go`
- Create: `internal/fanout/protocol/headers_test.go`
- Create: `internal/openai/sse.go`
- Create: `internal/openai/sse_test.go`
- Create: `internal/bench/client.go`
- Create: `internal/bench/client_test.go`
- Create: `cmd/fanoutbench/main.go`
- Create: `benchmarks/profiles/local-sim.yaml`

**Interfaces:**
- Consumes: OpenAI-compatible `/v1/chat/completions`, version-1 fan-out headers, and benchmark profile YAML.
- Produces: concurrent all-of-N groups, per-child TTFT/latency, group makespan, failure-preserving JSONL results, Prometheus metrics, and W3C trace context.

- [ ] **Step 1: Write failing header, streaming, and cancellation tests**

```go
func TestHeadersUseOneOpaqueGroupAndDistinctRequestIDs(t *testing.T) {
	group := protocol.NewGroup(8)
	require.Len(t, group.GroupID, 26)
	require.Equal(t, "1", group.Headers(0).Get("X-Vela-Fanout-Version"))
	require.NotEqual(t, group.Headers(0).Get("X-Request-Id"), group.Headers(1).Get("X-Request-Id"))
}

func TestSSECountsTTFTAtFirstContentDelta(t *testing.T) {
	stream := fixtureSSE("role-only", "first-content", "[DONE]")
	result, err := openai.ReadStream(stream, fixedClock())
	require.NoError(t, err)
	require.Equal(t, 250*time.Millisecond, result.TTFT)
}

func TestGroupFailureRetainsSuccessfulChildren(t *testing.T) {
	result := runAgainstServer(t, oneChildReturns503())
	require.Equal(t, evidence.OutcomeFailure, result.Outcome)
	require.Len(t, result.Children, 4)
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/fanout/protocol ./internal/openai ./internal/bench -count=1`

Expected: FAIL because packages are absent.

- [ ] **Step 3: Implement strict metadata generation and validation**

Group IDs use monotonic ULIDs from crypto/rand entropy; width is bounded by profile configuration. The client sends exactly the three Vela headers plus the pinned llm-d request ID header. No ordinal header is added.

- [ ] **Step 4: Implement a bounded SSE parser**

Support CRLF/LF, multi-line `data:`, comments, `[DONE]`, OpenAI error objects, and a configurable 8 MiB event limit. TTFT begins at dispatch and ends at the first non-empty content or output token, not a role-only chunk.

- [ ] **Step 5: Implement all-of-N execution and metrics**

Use `errgroup.WithContext` only for lifecycle; do not cancel successful siblings when one fails because failed groups still require complete evidence. Apply per-child deadlines, preserve every child result, and define makespan from first dispatch to last terminal child.

- [ ] **Step 6: Implement CLI profile expansion**

```bash
go run ./cmd/fanoutbench \
  --profile benchmarks/profiles/local-sim.yaml \
  --endpoint http://127.0.0.1:8000/v1/chat/completions \
  --output benchmarks/raw/local/groups.jsonl
```

The profile expands widths, prefix/output regimes, skews, cache states, load regimes, arms, randomized arm order, repetitions, and seed without changing the preregistered Z0 matrix.

- [ ] **Step 7: Verify GREEN**

Run: `go test ./internal/fanout/protocol ./internal/openai ./internal/bench -race -count=1`

Expected: PASS.

- [ ] **Step 8: Commit fanoutbench**

```bash
git add internal/fanout/protocol internal/openai internal/bench cmd/fanoutbench benchmarks/profiles
git commit -m "feat: add streaming best-of-N benchmark client"
```

### Task 7: Zeroing Placement Recorder and Z0-A/B/C Orchestrator

**Files:**
- Create: `research/placement-recorder/recorder.go`
- Create: `research/placement-recorder/recorder_test.go`
- Create: `research/placement-recorder/envoy.go`
- Create: `research/placement-recorder/envoy_test.go`
- Create: `research/placement-recorder/source.go`
- Create: `research/placement-recorder/source_test.go`
- Create: `cmd/zeroprobe/main.go`
- Create: `research/preregistration/README.md`

**Interfaces:**
- Consumes: Envoy JSON access logs, EPP structured logs/traces, fanoutbench results, and upstream Prometheus snapshots.
- Produces: correlated `PlacementEvent`, source-pressure observations, publication-delay observations, run manifests, and artifact-ledger entries.

- [ ] **Step 1: Write failing correlation and no-silent-drop tests**

```go
func TestCorrelateRequiresRequestAndGroupIdentity(t *testing.T) {
	_, err := recorder.Correlate(envoyLineWithoutRequestID(), benchChild())
	require.ErrorContains(t, err, "request_id")
}

func TestIngestReportsEveryUnmatchedLine(t *testing.T) {
	report, err := recorder.Ingest(context.Background(), fixtureLogsWithOneUnknown(), fixtureBench())
	require.NoError(t, err)
	require.Equal(t, 1, report.UnmatchedCount)
	require.FileExists(t, report.UnmatchedPath)
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./research/placement-recorder -count=1`

Expected: FAIL because recorder package is missing.

- [ ] **Step 3: Implement adapters around pinned upstream output**

Parse Envoy fields `START_TIME`, `REQ(X-REQUEST-ID)`, `UPSTREAM_HOST`, response code, duration, and EPP trace/span IDs. Parse pinned EPP scheduling records for candidate snapshots and inflight publication timestamps. Unknown formats are copied to `unmatched.jsonl` and make the run incomplete, never silently discarded.

- [ ] **Step 4: Implement source-pressure observation records**

Capture chosen source, candidate source count, transfer bytes, transfer start/end, last-sibling TTFT, and any available NIC/CPU-tier counters. Unavailable hardware counters are represented as absent fields, not zero.

- [ ] **Step 5: Implement `zeroprobe` phases**

```bash
zeroprobe run --phase z0-a --profile research/preregistration/z0-v1.yaml --artifact-root benchmarks/raw/z0
zeroprobe ingest --artifact-root benchmarks/raw/z0
zeroprobe analyze --artifact-root benchmarks/raw/z0
zeroprobe verify --artifact-root benchmarks/raw/z0
```

`run` creates a manifest before traffic, randomizes arm order from the registered seed, invokes fanoutbench, and captures logs/metrics. `analyze` invokes oracle replay and gate statistics but does not sign. `verify` validates schemas, SHA ledger, required cells, and failed-run retention.

- [ ] **Step 6: Verify GREEN**

Run: `go test ./research/placement-recorder -race -count=1`

Expected: PASS.

- [ ] **Step 7: Commit zeroprobe**

```bash
git add research/placement-recorder research/preregistration cmd/zeroprobe
git commit -m "feat: add reproducible Z0 recorder and orchestrator"
```

### Task 8: Deterministic Local Simulator and End-to-End Research Test

**Files:**
- Create: `internal/simfleet/fleet.go`
- Create: `internal/simfleet/fleet_test.go`
- Create: `internal/simfleet/openai.go`
- Create: `cmd/simfleet/main.go`
- Create: `tests/integration/local_zeroing_test.go`
- Create: `tests/fixtures/snapshots/eight-endpoints.jsonl`
- Create: `benchmarks/profiles/local-calibration.yaml`

**Interfaces:**
- Consumes: the same OpenAI and evidence contracts as real runs.
- Produces: deterministic eight-endpoint Arm A/B behavior, controllable cache/load/source-pressure states, SSE responses, and a complete local Z0 artifact bundle that is explicitly non-performance evidence.

- [ ] **Step 1: Write failing deterministic-fleet and E2E tests**

```go
func TestSameSeedProducesSamePlacementVector(t *testing.T) {
	a := simfleet.New(fixtureConfig(20260825))
	b := simfleet.New(fixtureConfig(20260825))
	require.Equal(t, runGroup(t, a, 16), runGroup(t, b, 16))
}

func TestLocalZeroingProducesVerifiedBundle(t *testing.T) {
	root := t.TempDir()
	runLocalZeroing(t, root)
	require.NoError(t, zeroprobe.Verify(root))
	require.FileExists(t, filepath.Join(root, "placements.jsonl"))
	require.FileExists(t, filepath.Join(root, "oracle.jsonl"))
	require.FileExists(t, filepath.Join(root, "ledger.jsonl"))
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/simfleet ./tests/integration -count=1`

Expected: FAIL because simulator is missing.

- [ ] **Step 3: Implement the simulator**

Expose eight logical endpoint IDs behind one HTTP server, configurable warm-prefix ownership, queue availability, delayed inflight publication, pull throughput, recompute throughput, output service time, and response failures. Add `X-Sim-Target` only in simulator mode and mark it forbidden in cloud manifests.

- [ ] **Step 4: Wire the local E2E path**

Start simfleet on an ephemeral port, execute registered Arm A/B cells for widths `2,4,8,16`, ingest events, replay the oracle, calculate but do not sign a gate preview, and verify the artifact ledger. The report must contain `evidence_scope: simulation_only`.

- [ ] **Step 5: Verify GREEN**

Run: `go test ./internal/simfleet ./tests/integration -race -count=1`

Expected: PASS.

- [ ] **Step 6: Commit simulator**

```bash
git add internal/simfleet cmd/simfleet tests/integration tests/fixtures benchmarks/profiles/local-calibration.yaml
git commit -m "test: add deterministic local zeroing environment"
```

### Task 9: Metrics, Tracing, and Cardinality Guardrails

**Files:**
- Create: `internal/metrics/metrics.go`
- Create: `internal/metrics/metrics_test.go`
- Create: `internal/trace/trace.go`
- Create: `internal/trace/trace_test.go`
- Create: `deploy/observability/grafana/velaserve.json`
- Create: `deploy/observability/prometheus-rules.yaml`
- Create: `deploy/observability/otel-collector.yaml`

**Interfaces:**
- Consumes: planner, benchmark, recorder, and gate lifecycle events.
- Produces: spec-named Prometheus metrics, bounded trace attributes, an importable Grafana dashboard, and static cardinality checks.

- [ ] **Step 1: Write failing forbidden-label and group-span tests**

```go
func TestMetricDescriptorsDoNotContainHighCardinalityLabels(t *testing.T) {
	for _, d := range metrics.Descriptors() {
		require.NotContains(t, d.Labels, "group_id")
		require.NotContains(t, d.Labels, "request_id")
		require.NotContains(t, d.Labels, "prompt_hash")
		require.NotContains(t, d.Labels, "endpoint_ip")
	}
}

func TestGroupTraceContainsRequiredChildSpans(t *testing.T) {
	spans := recordFixtureTrace(t)
	require.ElementsMatch(t, []string{"plan-preview", "scheduling", "queue-wait", "kv-acquisition", "inference", "stream-completion"}, childNames(spans))
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./internal/metrics ./internal/trace -count=1`

Expected: FAIL because observability packages are absent.

- [ ] **Step 3: Implement registered metrics and tracing helpers**

Use constant label sets for outcomes/reasons/operation; widths are histogram observations, not labels. Group and request IDs may be bounded span attributes and structured-log fields. Estimated prefill seconds has `estimate_model_revision` only in artifact data, not a metric label.

- [ ] **Step 4: Add dashboard and recording rules**

Dashboard panels cover makespan, slowest TTFT, recomputed tokens, placement vectors, oracle regret, scheduling overhead, ordinary-traffic latency, failure counts, and artifact completeness. No panel assumes Arm C/E exists.

- [ ] **Step 5: Verify GREEN and dashboard JSON**

Run: `go test ./internal/metrics ./internal/trace -count=1`

Expected: PASS.

Run: `jq -e . deploy/observability/grafana/velaserve.json >/dev/null`

Expected: exit 0.

- [ ] **Step 6: Commit observability**

```bash
git add internal/metrics internal/trace deploy/observability
git commit -m "feat: add bounded-cardinality VelaServe observability"
```

### Task 10: Kind, Gateway, and Pinned llm-d Local Wiring

**Files:**
- Create: `deploy/helm/velaserve/Chart.yaml`
- Create: `deploy/helm/velaserve/values.yaml`
- Create: `deploy/helm/velaserve/templates/fanoutbench-job.yaml`
- Create: `deploy/helm/velaserve/templates/zeroprobe-config.yaml`
- Create: `deploy/gateway/httproute.yaml`
- Create: `deploy/experiments/arm-a-values.yaml`
- Create: `deploy/experiments/arm-b-values.yaml`
- Create: `deploy/experiments/kind-values.yaml`
- Create: `hack/kind-up.sh`
- Create: `hack/kind-down.sh`
- Create: `hack/verify-render.sh`
- Create: `tests/integration/manifests_test.go`

**Interfaces:**
- Consumes: frozen upstream images/commits, fanoutbench/zeroprobe images, and llm-d simulator endpoints.
- Produces: one- and two-EPP local configurations, Envoy/Gateway routing, upstream affinity and load-aware+P2P arms, and rendered manifests with no Valkey or VelaServe picker.

- [ ] **Step 1: Write failing manifest invariants**

```go
func TestOrdinaryAndFanoutStageOneChartsContainNoCoordinationStore(t *testing.T) {
	objects := renderChart(t, "deploy/experiments/kind-values.yaml")
	require.False(t, hasContainerOrService(objects, "valkey"))
	require.False(t, hasEnv(objects, "VELASERVE_PLACEMENT_ENABLED", "true"))
}

func TestArmsPinSameImagesAndDifferOnlyByRoutingProfile(t *testing.T) {
	a := renderValues(t, "deploy/experiments/arm-a-values.yaml")
	b := renderValues(t, "deploy/experiments/arm-b-values.yaml")
	require.Equal(t, imageSet(a), imageSet(b))
	require.Equal(t, []string{"routingProfile"}, semanticDiffKeys(a, b))
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./tests/integration -run 'Test.*Chart|TestArms' -count=1`

Expected: FAIL because charts are absent.

- [ ] **Step 3: Create charts and explicit Arm A/B configs**

Arm A enables precise-prefix-cache affinity and existing P2P source selection. Arm B enables upstream load-aware scheduling and the same P2P settings. Two-EPP mode explicitly sets active-active only for Z0 dispersion observation and records the chart warning; it does not add local-state plugins.

- [ ] **Step 4: Add repeatable Kind lifecycle**

`kind-up.sh` verifies Docker, Kind, Helm, kubectl versions; creates cluster `velaserve-z0`; installs Gateway API CRDs and pinned llm-d assets; waits for readiness; prints the fanoutbench endpoint. `kind-down.sh` only deletes cluster `velaserve-z0` and requires an exact current-context match.

- [ ] **Step 5: Render and run local smoke tests**

Run: `bash hack/verify-render.sh`

Expected: every Helm/Kustomize render succeeds and image refs are immutable.

Run: `go test ./tests/integration -run 'Test.*Chart|TestArms' -count=1`

Expected: PASS.

Run when Docker is available: `bash hack/kind-up.sh && go test ./tests/integration -run TestKindUpstreamSSE -count=1 && bash hack/kind-down.sh`

Expected: Kind smoke PASS and cluster cleanup PASS.

- [ ] **Step 6: Commit local deployment**

```bash
git add deploy/helm deploy/gateway deploy/experiments hack tests/integration
git commit -m "feat: add pinned llm-d Kind zeroing stack"
```

### Task 11: AWS EKS Terraform and Cloud Experiment Handoff

**Files:**
- Create: `infra/terraform/versions.tf`
- Create: `infra/terraform/providers.tf`
- Create: `infra/terraform/variables.tf`
- Create: `infra/terraform/main.tf`
- Create: `infra/terraform/outputs.tf`
- Create: `infra/terraform/terraform.tfvars.example`
- Create: `infra/terraform/README.md`
- Create: `deploy/experiments/aws-z0-values.yaml`
- Create: `hack/cloud-preflight.sh`
- Create: `hack/cloud-run-z0.sh`
- Create: `hack/cloud-collect.sh`
- Create: `hack/cloud-cleanup.sh`
- Create: `tests/static/cloud_assets_test.go`

**Interfaces:**
- Consumes: operator-supplied AWS account, region, GPU quota, model repository credentials, and exact GPU shape.
- Produces: VPC/EKS/Karpenter/ECR/S3/IAM/security-group plans, six-to-eight homogeneous GPU replica settings, reproducible Z0 commands, artifact collection, and scoped cleanup. This task never applies Terraform.

- [ ] **Step 1: Write failing static safety tests**

```go
func TestTerraformDefaultsDoNotCreateResourcesWithoutExplicitApply(t *testing.T) {
	vars := parseVariables(t)
	require.Equal(t, 0, vars["gpu_desired_size"].Default)
	require.Equal(t, 6, vars["gpu_minimum_benchmark_replicas"].Default)
}

func TestCleanupRequiresExactClusterAndArtifactConfirmation(t *testing.T) {
	script := read(t, "hack/cloud-cleanup.sh")
	require.Contains(t, script, "VELASERVE_CLUSTER_NAME")
	require.Contains(t, script, "VELASERVE_ARTIFACTS_COLLECTED=true")
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `go test ./tests/static -count=1`

Expected: FAIL because assets do not exist.

- [ ] **Step 3: Implement composable Terraform without apply wrappers**

Pin Terraform and AWS/Kubernetes/Helm providers. Create one VPC, public/private subnets, EKS control plane, CPU managed node group, Karpenter prerequisites, ECR repositories, versioned/encrypted S3 artifact bucket, least-privilege roles, and GPU security groups. GPU NodePool defaults to zero desired capacity and accepts a quota-confirmed instance allowlist whose first documented choice is a G6e single-L40S shape.

- [ ] **Step 4: Implement explicit cloud preflight and Z0 scripts**

Preflight checks AWS identity, region, quota, six-replica minimum, kubectl context, image digests, P2P transport, model revision, artifact bucket, and that the preregistration hash matches Git. `cloud-run-z0.sh` refuses to run if any check fails. `cloud-collect.sh` copies raw JSONL, manifests, logs, metrics, and ledger before permitting cleanup.

- [ ] **Step 5: Validate without cloud access**

Run: `terraform -chdir=infra/terraform fmt -check -recursive`

Expected: exit 0.

Run: `terraform -chdir=infra/terraform init -backend=false && terraform -chdir=infra/terraform validate`

Expected: `Success! The configuration is valid.`

Run: `go test ./tests/static -count=1`

Expected: PASS.

- [ ] **Step 6: Commit cloud handoff**

```bash
git add infra deploy/experiments/aws-z0-values.yaml hack/cloud-*.sh tests/static
git commit -m "feat: add safe EKS zeroing experiment handoff"
```

### Task 12: Public Documentation, CI, and Stage-1 Completion Audit

**Files:**
- Create: `README.md`
- Create: `LICENSE`
- Create: `CONTRIBUTING.md`
- Create: `SECURITY.md`
- Create: `docs/architecture.md`
- Create: `docs/state-consistency.md`
- Create: `docs/benchmark-methodology.md`
- Create: `docs/failure-analysis.md`
- Create: `docs/performance-report.md`
- Create: `docs/cloud-handoff.md`
- Create: `docs/upstream-version-matrix.md`
- Create: `docs/gate-status.md`
- Create: `scripts/verify-stage1.sh`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: every prior task and the v3 portfolio deliverables.
- Produces: a public, reproducible repository that clearly states what is measured, simulated, pending, gate-forbidden, and ready for the operator's AWS Z0 run.

- [ ] **Step 1: Write the completion verifier before final documentation**

`scripts/verify-stage1.sh` runs formatting, vet, unit/property/integration/static tests, race tests, schema checks, local zeroing, artifact verification, dashboard JSON validation, Helm/Kustomize rendering, Terraform formatting/validation, secret scanning, forbidden-placeholder scanning, and `git diff --check`. It exits non-zero when Kind/Docker-required verification is skipped unless `VELASERVE_ALLOW_NO_DOCKER=1` is explicitly set and recorded in `verification.json`.

- [ ] **Step 2: Run verifier and record the expected failures**

Run: `bash scripts/verify-stage1.sh`

Expected: FAIL listing missing documentation and CI coverage.

- [ ] **Step 3: Write evidence-first public documentation**

README leads with the falsifiable question, shows `gate status: NOT RUN ON REAL GPU`, separates simulation from measurement, documents one-command local zeroing, and links the cloud handoff. `docs/gate-status.md` states that no placement/source-pressure production code exists. `performance-report.md` contains the fixed plot/table schema and empty evidence slots labeled `awaiting real-GPU Z0`, not invented values.

- [ ] **Step 4: Configure CI**

CI jobs are `go-test`, `race-and-property`, `schemas-and-local-zeroing`, `render-manifests`, `terraform-validate`, `security`, and `stage1-audit`. Cache keys include `go.sum`, frozen tool versions, and upstream lock hash. No CI job uses AWS credentials.

- [ ] **Step 5: Run full verification**

Run: `bash scripts/verify-stage1.sh`

Expected: PASS, or PASS with one explicit Docker/Kind waiver recorded when the local engine is unavailable. All non-container checks must pass without waivers.

Run: `git grep -nE 'TBD|TODO|implement later|fill in details' -- ':!docs/superpowers/plans/*'`

Expected: no matches.

Run: `git grep -nE 'Valkey|CreatePlan|ClaimSlot|ReservePull' -- '*.go'`

Expected: no production-code matches; references may exist only in tests that assert absence or in docs.

- [ ] **Step 6: Commit documentation and audit**

```bash
git add README.md LICENSE CONTRIBUTING.md SECURITY.md docs scripts .github/workflows/ci.yml
git commit -m "docs: publish VelaServe stage-one research handoff"
```

- [ ] **Step 7: Create and push the public repository**

```bash
gh repo create JDinSeattle/velaserve --public --source=. --remote=origin --description "Fan-out-aware scheduling research for agentic vLLM serving"
git push -u origin main
gh repo view JDinSeattle/velaserve --json nameWithOwner,visibility,url,defaultBranchRef
```

Expected: `nameWithOwner` is `JDinSeattle/velaserve`, visibility is `PUBLIC`, and default branch is `main`.

- [ ] **Step 8: Freeze the Stage-1 handoff tag**

```bash
git tag -s stage1-z0-ready -m "VelaServe Stage 1: real-GPU Z0 ready"
git push origin stage1-z0-ready
git status --short --branch
```

Expected: signed tag is on the verified commit and working tree is clean.

## Plan Self-Review Record

- Spec coverage: Tasks 1-9 cover the always-required research, simulator, oracle, benchmark, observability, and evidence discipline; Tasks 10-11 cover local and cloud handoff; Task 12 covers public portfolio deliverables and reproducibility.
- Gate fidelity: production placement, Valkey state, source permits, and EPP fan-out integration are intentionally absent until real-GPU Z0 evidence is verified and signed.
- Type consistency: evidence records flow from Task 1 through replay, statistics, benchmark, recorder, simulator, and the gate command without parallel shadow schemas.
- No-placeholder scan: executable artifacts must contain no placeholders; the plan uses exact commands, types, thresholds, widths, skews, seeds, output paths, and expected results.
- Follow-up boundary: a verified `placement` decision creates a placement implementation plan; a verified `source-pressure` decision creates a source-control plan; `negative-result` creates only the final negative-result report and no coordination package.
