// Package simfleet provides a deterministic functional stand-in for eight
// logical vLLM replicas. Its timings are simulation-only and must not be used
// as performance evidence.
package simfleet

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

type Config struct {
	Seed                     uint64
	EndpointCount            uint32
	Model                    string
	WarmOwner                uint32
	PrefixTokens             uint64
	InflightPublicationDelay time.Duration
	AffinityLoadGate         time.Duration
	ServiceTime              time.Duration
	LocalTTFT                time.Duration
	PullTTFT                 time.Duration
	RecomputeTTFT            time.Duration
	OutputDelay              time.Duration
	FailureSlots             map[uint32]int
}

func DefaultConfig() Config {
	return Config{
		Seed:                     20260825,
		EndpointCount:            8,
		Model:                    "velaserve-simulator",
		WarmOwner:                0,
		PrefixTokens:             4096,
		InflightPublicationDelay: 5 * time.Millisecond,
		AffinityLoadGate:         50 * time.Millisecond,
		ServiceTime:              10 * time.Millisecond,
		LocalTTFT:                time.Millisecond,
		PullTTFT:                 3 * time.Millisecond,
		RecomputeTTFT:            5 * time.Millisecond,
		OutputDelay:              time.Millisecond,
		FailureSlots:             map[uint32]int{},
	}
}

type GroupInput struct {
	Arm         evidence.Arm
	Width       uint32
	ArrivalSkew time.Duration
}

type endpoint struct {
	ref          evidence.EndpointRef
	availability time.Duration
}

type Fleet struct {
	config    Config
	endpoints []endpoint

	mu           sync.Mutex
	plans        map[string][]string
	eppRecords   []placementrecorder.EPPRecord
	envoyRecords []placementrecorder.EnvoyRecord
}

func New(config Config) (*Fleet, error) {
	if config.Seed == 0 {
		return nil, fmt.Errorf("seed must be positive")
	}
	if config.EndpointCount != 8 {
		return nil, fmt.Errorf("endpoint count: got %d, want frozen simulator value 8", config.EndpointCount)
	}
	if config.WarmOwner >= config.EndpointCount || config.Model == "" || config.PrefixTokens == 0 {
		return nil, fmt.Errorf("model, prefix tokens, and warm owner are invalid")
	}
	if config.InflightPublicationDelay < 0 || config.AffinityLoadGate < 0 || config.ServiceTime <= 0 || config.LocalTTFT < 0 || config.PullTTFT < 0 || config.RecomputeTTFT < 0 || config.OutputDelay < 0 {
		return nil, fmt.Errorf("simulator durations are invalid")
	}
	state := config.Seed
	endpoints := make([]endpoint, config.EndpointCount)
	for index := range endpoints {
		state = xorshift64(state)
		jitter := time.Duration(state%4) * time.Millisecond
		endpoints[index] = endpoint{
			ref:          evidence.EndpointRef{ID: fmt.Sprintf("sim-%d", index), Model: config.Model, Zone: fmt.Sprintf("sim-zone-%d", index%2)},
			availability: jitter,
		}
	}
	return &Fleet{config: config, endpoints: endpoints, plans: make(map[string][]string)}, nil
}

func (fleet *Fleet) PlacementVector(input GroupInput) ([]string, error) {
	if err := validateGroupInput(input); err != nil {
		return nil, err
	}
	if input.Arm == evidence.ArmAffinityP2P && fleet.endpoints[fleet.config.WarmOwner].availability <= fleet.config.AffinityLoadGate {
		vector := make([]string, input.Width)
		for index := range vector {
			vector[index] = fleet.endpoints[fleet.config.WarmOwner].ref.ID
		}
		return vector, nil
	}
	if input.Arm != evidence.ArmLoadAwareP2P && input.Arm != evidence.ArmLoadAwareNoP2P && input.Arm != evidence.ArmAffinityP2P {
		return nil, fmt.Errorf("arm %q is not a frozen simulator baseline", input.Arm)
	}

	published := make([]uint32, len(fleet.endpoints))
	type pendingPublication struct {
		endpoint int
		visible  time.Duration
	}
	pending := make([]pendingPublication, 0, input.Width)
	vector := make([]string, input.Width)
	for slot := uint32(0); slot < input.Width; slot++ {
		arrival := time.Duration(slot) * input.ArrivalSkew
		remaining := pending[:0]
		for _, publication := range pending {
			if publication.visible <= arrival {
				published[publication.endpoint]++
			} else {
				remaining = append(remaining, publication)
			}
		}
		pending = remaining
		selected := 0
		selectedScore := fleet.endpoints[0].availability + time.Duration(published[0])*fleet.config.ServiceTime
		for index := 1; index < len(fleet.endpoints); index++ {
			score := fleet.endpoints[index].availability + time.Duration(published[index])*fleet.config.ServiceTime
			if score < selectedScore || (score == selectedScore && fleet.endpoints[index].ref.ID < fleet.endpoints[selected].ref.ID) {
				selected = index
				selectedScore = score
			}
		}
		vector[slot] = fleet.endpoints[selected].ref.ID
		pending = append(pending, pendingPublication{endpoint: selected, visible: arrival + fleet.config.InflightPublicationDelay})
	}
	return vector, nil
}

func (fleet *Fleet) Snapshot(observedAt time.Time) evidence.EndpointSnapshot {
	states := make([]evidence.EndpointState, len(fleet.endpoints))
	owner := fleet.endpoints[fleet.config.WarmOwner].ref
	for index, endpoint := range fleet.endpoints {
		state := evidence.EndpointState{
			Ref:                endpoint.ref,
			Healthy:            true,
			Compatible:         true,
			AvailableAtSeconds: endpoint.availability.Seconds(),
			LocalPrefixTokens:  0,
		}
		if uint32(index) == fleet.config.WarmOwner {
			state.LocalPrefixTokens = fleet.config.PrefixTokens
		} else {
			state.P2PSources = []evidence.PrefixSource{{
				Source:        owner,
				CachedTokens:  fleet.config.PrefixTokens,
				TransferBytes: fleet.config.PrefixTokens * 512,
			}}
		}
		states[index] = state
	}
	return evidence.EndpointSnapshot{ObservedAt: observedAt.UTC(), Endpoints: states}
}

func (fleet *Fleet) EPPRecords() []placementrecorder.EPPRecord {
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	clone := append([]placementrecorder.EPPRecord(nil), fleet.eppRecords...)
	sort.Slice(clone, func(i, j int) bool { return clone[i].RequestID < clone[j].RequestID })
	return clone
}

func (fleet *Fleet) EnvoyRecords() []placementrecorder.EnvoyRecord {
	fleet.mu.Lock()
	defer fleet.mu.Unlock()
	clone := append([]placementrecorder.EnvoyRecord(nil), fleet.envoyRecords...)
	sort.Slice(clone, func(i, j int) bool { return clone[i].RequestID < clone[j].RequestID })
	return clone
}

func validateGroupInput(input GroupInput) error {
	switch input.Width {
	case 2, 4, 8, 16:
	default:
		return fmt.Errorf("width %d is outside the frozen Z0 matrix", input.Width)
	}
	switch input.ArrivalSkew {
	case 0, time.Millisecond, 5 * time.Millisecond, 20 * time.Millisecond:
	default:
		return fmt.Errorf("arrival skew %s is outside the frozen Z0 matrix", input.ArrivalSkew)
	}
	return nil
}

func xorshift64(value uint64) uint64 {
	value ^= value << 13
	value ^= value >> 7
	value ^= value << 17
	return value
}
