package evidence

import "time"

const SchemaVersion = "velaserve.evidence/v1"

type Arm string

const (
	ArmAffinityP2P      Arm = "arm-a-affinity-p2p"
	ArmLoadAwareP2P     Arm = "arm-b-load-aware-p2p"
	ArmVelaPlacementP2P Arm = "arm-c-velaserve-placement-p2p"
	ArmLoadAwareNoP2P   Arm = "arm-d-load-aware-no-p2p"
	ArmSourceControlP2P Arm = "arm-e-source-control-p2p"
	ArmNawareOracle     Arm = "offline-n-aware-oracle"
)

type LoadRegime string

const (
	LoadIdle           LoadRegime = "idle"
	LoadModerate       LoadRegime = "moderate"
	LoadNearSaturation LoadRegime = "near-saturation"
)

type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeFailure   Outcome = "failure"
	OutcomeCancelled Outcome = "cancelled"
)

type GateBranch string

const (
	BranchPlacement            GateBranch = "placement"
	BranchSourcePressure       GateBranch = "source-pressure"
	BranchNegativeResult       GateBranch = "negative-result"
	BranchInsufficientEvidence GateBranch = "insufficient-evidence"
)

type EndpointRef struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Zone  string `json:"zone,omitempty"`
}

type PrefixSource struct {
	Source        EndpointRef `json:"source"`
	CachedTokens  uint64      `json:"cached_tokens"`
	TransferBytes uint64      `json:"transfer_bytes"`
}

type EndpointState struct {
	Ref                 EndpointRef    `json:"ref"`
	Healthy             bool           `json:"healthy"`
	Compatible          bool           `json:"compatible"`
	AvailableAtSeconds  float64        `json:"available_at_seconds"`
	QueueDepth          uint32         `json:"queue_depth"`
	RunningRequests     uint32         `json:"running_requests"`
	LocalPrefixTokens   uint64         `json:"local_prefix_tokens"`
	SourcePrefixTokens  uint64         `json:"source_prefix_tokens"`
	ObservedInflight    uint32         `json:"observed_inflight"`
	InflightPublishedAt *time.Time     `json:"inflight_published_at,omitempty"`
	P2PSources          []PrefixSource `json:"p2p_sources,omitempty"`
}

type EndpointSnapshot struct {
	ObservedAt time.Time       `json:"observed_at"`
	Endpoints  []EndpointState `json:"endpoints"`
}

type EndpointScore struct {
	Endpoint EndpointRef `json:"endpoint"`
	Score    float64     `json:"score"`
}

type PlacementEvent struct {
	SchemaVersion         string            `json:"schema_version"`
	RunID                 string            `json:"run_id"`
	Arm                   Arm               `json:"arm"`
	GroupID               string            `json:"group_id"`
	RequestID             string            `json:"request_id"`
	FanoutWidth           uint32            `json:"fanout_width"`
	ArrivalSkewMS         uint32            `json:"arrival_skew_ms"`
	EPPReplicas           uint32            `json:"epp_replicas"`
	LoadRegime            LoadRegime        `json:"load_regime"`
	Snapshot              EndpointSnapshot  `json:"snapshot"`
	Target                EndpointRef       `json:"target"`
	ScoredCandidates      []EndpointScore   `json:"scored_candidates,omitempty"`
	SelectedP2PSource     *PrefixSource     `json:"selected_p2p_source,omitempty"`
	SelectedP2PSourceHost string            `json:"selected_p2p_source_host,omitempty"`
	SelectedP2PSourcePort uint32            `json:"selected_p2p_source_port,omitempty"`
	ObservedAt            time.Time         `json:"observed_at"`
	Attributes            map[string]string `json:"attributes,omitempty"`
}

type ChildResult struct {
	RequestID      string       `json:"request_id"`
	Target         *EndpointRef `json:"target,omitempty"`
	TTFTSeconds    float64      `json:"ttft_seconds"`
	LatencySeconds float64      `json:"latency_seconds"`
	OutputTokens   uint64       `json:"output_tokens"`
	PromptTokens   uint64       `json:"prompt_tokens,omitempty"`
	CachedTokens   *uint64      `json:"cached_tokens,omitempty"`
	Outcome        Outcome      `json:"outcome"`
	Failure        string       `json:"failure,omitempty"`
	DispatchedAt   *time.Time   `json:"dispatched_at,omitempty"`
	FirstTokenAt   *time.Time   `json:"first_token_at,omitempty"`
	CompletedAt    *time.Time   `json:"completed_at,omitempty"`
}

// BenchmarkCell is the frozen workload coordinate needed to reproduce and
// pair a group result. Prompt text is intentionally excluded from evidence.
type BenchmarkCell struct {
	Repetition    uint32     `json:"repetition"`
	PrefixRegime  string     `json:"prefix_regime"`
	PrefixTokens  uint64     `json:"prefix_tokens"`
	OutputRegime  string     `json:"output_regime"`
	MaxTokens     uint32     `json:"max_tokens"`
	ArrivalSkewMS uint32     `json:"arrival_skew_ms"`
	LoadRegime    LoadRegime `json:"load_regime"`
	CacheState    string     `json:"cache_state"`
	Transport     string     `json:"transport"`
	EPPReplicas   uint32     `json:"epp_replicas"`
}

const ConditionAttestationSchemaVersion = "velaserve.condition-attestation/v1"

type ConditionAttestation struct {
	SchemaVersion      string                 `json:"schema_version"`
	RunID              string                 `json:"run_id"`
	GroupID            string                 `json:"group_id"`
	LoadRegime         LoadRegime             `json:"load_regime"`
	CacheState         string                 `json:"cache_state"`
	PrefixSourceCount  uint32                 `json:"prefix_source_count,omitempty"`
	OwnerRotation      uint32                 `json:"owner_rotation"`
	ControllerRevision string                 `json:"controller_revision"`
	StateSHA256        string                 `json:"state_sha256"`
	ObservedState      ConditionObservedState `json:"observed_state"`
	AppliedAt          time.Time              `json:"applied_at"`
	FinalizedAt        *time.Time             `json:"finalized_at"`
}

type ConditionObservedState struct {
	OfferedLoadQPS                    float64  `json:"offered_load_qps"`
	AchievedLoadQPS                   float64  `json:"achieved_load_qps"`
	SaturationQPS                     float64  `json:"saturation_qps"`
	LoadProfileSHA256                 string   `json:"load_profile_sha256"`
	LoadProfilesSHA256                string   `json:"load_profiles_sha256"`
	LoadCalibrationSHA256             string   `json:"load_calibration_sha256"`
	CachedEndpointIDs                 []string `json:"cached_endpoint_ids"`
	PrefixSourceEndpointIDs           []string `json:"prefix_source_endpoint_ids,omitempty"`
	OwnerEndpointOrder                []string `json:"owner_endpoint_order"`
	DrainedEndpointIDs                []string `json:"drained_endpoint_ids"`
	DrainStableSamples                uint32   `json:"drain_stable_samples"`
	DroppedLoadRequests               uint64   `json:"dropped_load_requests"`
	OrdinaryTrafficMeanLatencySeconds float64  `json:"ordinary_traffic_mean_latency_seconds"`
	MeasurementSource                 string   `json:"measurement_source"`
}

type GroupResult struct {
	SchemaVersion           string                `json:"schema_version"`
	RunID                   string                `json:"run_id"`
	Arm                     Arm                   `json:"arm"`
	GroupID                 string                `json:"group_id"`
	FanoutWidth             uint32                `json:"fanout_width"`
	MakespanSeconds         float64               `json:"makespan_seconds"`
	SlowestChildTTFTSeconds float64               `json:"slowest_child_ttft_seconds"`
	RecomputedPrefixTokens  *uint64               `json:"recomputed_prefix_tokens,omitempty"`
	EstimatedPrefillSeconds *float64              `json:"estimated_prefill_gpu_seconds,omitempty"`
	Cell                    BenchmarkCell         `json:"cell"`
	Outcome                 Outcome               `json:"outcome"`
	Failure                 string                `json:"failure,omitempty"`
	Children                []ChildResult         `json:"children"`
	Condition               *ConditionAttestation `json:"condition,omitempty"`
	ConditionFailure        string                `json:"condition_failure,omitempty"`
}

type ConfidenceInterval struct {
	Estimate float64 `json:"estimate"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
}

type GateEvidenceCell struct {
	FanoutWidth          uint32             `json:"fanout_width"`
	LoadRegime           LoadRegime         `json:"load_regime"`
	PrefixRegime         string             `json:"prefix_regime"`
	Transport            string             `json:"transport"`
	Metric               string             `json:"metric"`
	BaselineSourceCount  uint32             `json:"baseline_source_count,omitempty"`
	CandidateSourceCount uint32             `json:"candidate_source_count,omitempty"`
	Pairs                uint32             `json:"pairs"`
	Improvement          ConfidenceInterval `json:"improvement"`
}

type GateDecision struct {
	SchemaVersion         string             `json:"schema_version"`
	DecisionID            string             `json:"decision_id"`
	PreregistrationSHA256 string             `json:"preregistration_sha256"`
	ArtifactLedgerSHA256  string             `json:"artifact_ledger_sha256"`
	EvidenceSHA256        string             `json:"evidence_sha256"`
	Threshold             float64            `json:"threshold"`
	Confidence            float64            `json:"confidence"`
	Branch                GateBranch         `json:"branch"`
	QualifyingWidths      []uint32           `json:"qualifying_widths"`
	Evidence              []GateEvidenceCell `json:"evidence"`
	DecidedAt             time.Time          `json:"decided_at"`
}
