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
	ObservedInflight    uint32         `json:"observed_inflight"`
	InflightPublishedAt *time.Time     `json:"inflight_published_at,omitempty"`
	P2PSources          []PrefixSource `json:"p2p_sources,omitempty"`
}

type EndpointSnapshot struct {
	ObservedAt time.Time       `json:"observed_at"`
	Endpoints  []EndpointState `json:"endpoints"`
}

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

type ChildResult struct {
	RequestID      string      `json:"request_id"`
	Target         EndpointRef `json:"target"`
	TTFTSeconds    float64     `json:"ttft_seconds"`
	LatencySeconds float64     `json:"latency_seconds"`
	OutputTokens   uint64      `json:"output_tokens"`
	Outcome        Outcome     `json:"outcome"`
	Failure        string      `json:"failure,omitempty"`
	DispatchedAt   time.Time   `json:"dispatched_at,omitempty"`
	FirstTokenAt   time.Time   `json:"first_token_at,omitempty"`
	CompletedAt    time.Time   `json:"completed_at,omitempty"`
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

type ConfidenceInterval struct {
	Estimate float64 `json:"estimate"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
}

type GateEvidenceCell struct {
	FanoutWidth  uint32             `json:"fanout_width"`
	LoadRegime   LoadRegime         `json:"load_regime"`
	PrefixRegime string             `json:"prefix_regime"`
	Transport    string             `json:"transport"`
	Metric       string             `json:"metric"`
	Pairs        uint32             `json:"pairs"`
	Improvement  ConfidenceInterval `json:"improvement"`
}

type GateDecision struct {
	SchemaVersion         string             `json:"schema_version"`
	DecisionID            string             `json:"decision_id"`
	PreregistrationSHA256 string             `json:"preregistration_sha256"`
	ArtifactLedgerSHA256  string             `json:"artifact_ledger_sha256"`
	Threshold             float64            `json:"threshold"`
	Confidence            float64            `json:"confidence"`
	Branch                GateBranch         `json:"branch"`
	QualifyingWidths      []uint32           `json:"qualifying_widths"`
	Evidence              []GateEvidenceCell `json:"evidence"`
	DecidedAt             time.Time          `json:"decided_at"`
}
