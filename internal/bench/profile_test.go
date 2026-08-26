package bench

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestExpandProfileCoversFrozenWidthsAndSkewsDeterministically(t *testing.T) {
	profile := validProfile()
	first, err := ExpandProfile(profile)
	if err != nil {
		t.Fatalf("ExpandProfile() error = %v", err)
	}
	second, err := ExpandProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same seed did not produce the same workload order")
	}
	if len(first) != 32 {
		t.Fatalf("expanded groups = %d, want 2 arms * 4 widths * 4 skews", len(first))
	}
	seenWidths := map[int]bool{}
	seenSkews := map[uint32]bool{}
	for _, request := range first {
		seenWidths[len(request.Suffixes)] = true
		seenSkews[request.Cell.ArrivalSkewMS] = true
		if request.Cell.PrefixRegime == "" || request.Cell.Repetition != 1 {
			t.Fatalf("missing workload coordinate: %#v", request.Cell)
		}
		if request.RunID != "local-z0" {
			t.Fatalf("group escaped experiment run identity: %q", request.RunID)
		}
	}
	if !reflect.DeepEqual(seenWidths, map[int]bool{2: true, 4: true, 8: true, 16: true}) {
		t.Fatalf("widths = %v", seenWidths)
	}
	if !reflect.DeepEqual(seenSkews, map[uint32]bool{0: true, 1: true, 5: true, 20: true}) {
		t.Fatalf("skews = %v", seenSkews)
	}
}

func TestValidateAWSZ0ProfileRejectsReducedRepetitions(t *testing.T) {
	profile, err := LoadProfile(filepath.Join("..", "..", "benchmarks", "profiles", "aws-z0-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	profile.Repetitions = 1
	if err := ValidateAWSZ0Profile(profile, "z0-b"); err == nil {
		t.Fatal("ValidateAWSZ0Profile() accepted a reduced repetition count")
	}
}

func TestValidateAWSZ0ProfileRejectsOmittedFactor(t *testing.T) {
	profile, err := LoadProfile(filepath.Join("..", "..", "benchmarks", "profiles", "aws-z0-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	profile.LoadRegimes = profile.LoadRegimes[:2]
	if err := ValidateAWSZ0Profile(profile, "z0-b"); err == nil {
		t.Fatal("ValidateAWSZ0Profile() accepted an omitted load regime")
	}
}

func TestValidateAWSZ0ProfileAcceptsFrozenZ0CCacheSlice(t *testing.T) {
	profile, err := LoadProfile(filepath.Join("..", "..", "benchmarks", "profiles", "aws-z0-template.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	profile.CacheStates = []string{"distributed-warm"}
	profile.PrefixSourceCounts = []uint32{1, 2, 4}
	if err := ValidateAWSZ0Profile(profile, "z0-c"); err != nil {
		t.Fatalf("ValidateAWSZ0Profile() error = %v", err)
	}
}

func TestExpandProfileInterleavesEverySourceCountWithinEachCoordinate(t *testing.T) {
	profile := validProfile()
	profile.Arms = []evidence.Arm{evidence.ArmLoadAwareP2P}
	profile.PrefixSourceCounts = []uint32{1, 2, 4}
	requests, err := ExpandProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 48 {
		t.Fatalf("expanded groups = %d, want 16 coordinates * 3 source counts", len(requests))
	}
	rotationCounts := map[uint32]int{}
	for start := 0; start < len(requests); start += 3 {
		seen := map[uint32]bool{}
		rotation := requests[start].OwnerRotation
		for _, request := range requests[start : start+3] {
			seen[request.PrefixSourceCount] = true
			if request.OwnerRotation != rotation {
				t.Fatalf("coordinate %d owner rotations differ: %d and %d", start/3, rotation, request.OwnerRotation)
			}
		}
		if !reflect.DeepEqual(seen, map[uint32]bool{1: true, 2: true, 4: true}) {
			t.Fatalf("coordinate %d source-count block = %v", start/3, seen)
		}
		rotationCounts[rotation%4]++
	}
	if !reflect.DeepEqual(rotationCounts, map[uint32]int{0: 4, 1: 4, 2: 4, 3: 4}) {
		t.Fatalf("owner rotations are not balanced across four replicas: %v", rotationCounts)
	}
}

func TestExpandProfileRejectsChangedZ0WidthMatrix(t *testing.T) {
	profile := validProfile()
	profile.Widths = []uint32{2, 3, 8, 16}
	if _, err := ExpandProfile(profile); err == nil {
		t.Fatal("ExpandProfile() accepted a changed Z0 width matrix")
	}
}

func validProfile() Profile {
	return Profile{
		SchemaVersion:  BenchmarkProfileSchemaVersion,
		Seed:           20260825,
		RunIDPrefix:    "local-z0",
		Model:          "sim-model",
		MaxWidth:       16,
		TimeoutMS:      2000,
		Repetitions:    1,
		Widths:         []uint32{2, 4, 8, 16},
		ArrivalSkewsMS: []uint32{0, 1, 5, 20},
		Arms:           []evidence.Arm{evidence.ArmAffinityP2P, evidence.ArmLoadAwareP2P},
		Prefixes: []PrefixProfile{{
			ID:              "near-crossover",
			EstimatedTokens: 4096,
			RepeatText:      "shared context ",
			RepeatCount:     4,
		}},
		Outputs:     []OutputProfile{{ID: "short", MaxTokens: 8}},
		LoadRegimes: []evidence.LoadRegime{evidence.LoadModerate},
		CacheStates: []string{"warm-owner"},
		Transports:  []string{"tcp"},
		EPPReplicas: []uint32{1},
		Suffixes:    []string{"candidate 01", "candidate 02", "candidate 03", "candidate 04", "candidate 05", "candidate 06", "candidate 07", "candidate 08", "candidate 09", "candidate 10", "candidate 11", "candidate 12", "candidate 13", "candidate 14", "candidate 15", "candidate 16"},
	}
}
