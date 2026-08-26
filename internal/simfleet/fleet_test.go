package simfleet

import (
	"reflect"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestSameSeedProducesSamePlacementVector(t *testing.T) {
	config := DefaultConfig()
	config.Seed = 20260825
	first, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	input := GroupInput{Arm: evidence.ArmLoadAwareP2P, Width: 16, ArrivalSkew: 20 * time.Millisecond}
	firstVector, err := first.PlacementVector(input)
	if err != nil {
		t.Fatal(err)
	}
	secondVector, err := second.PlacementVector(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstVector, secondVector) {
		t.Fatalf("placement vectors differ:\n%v\n%v", firstVector, secondVector)
	}
}

func TestAffinityAndLoadAwarePoliciesRemainDistinct(t *testing.T) {
	fleet, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	affinity, err := fleet.PlacementVector(GroupInput{Arm: evidence.ArmAffinityP2P, Width: 8})
	if err != nil {
		t.Fatal(err)
	}
	loadAware, err := fleet.PlacementVector(GroupInput{Arm: evidence.ArmLoadAwareP2P, Width: 8, ArrivalSkew: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(affinity, loadAware) {
		t.Fatalf("frozen baseline policies collapsed to one vector: %v", affinity)
	}
}
