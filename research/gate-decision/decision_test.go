package gate

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

func TestPlacementNeedsTwoAdjacentPassingWidthsInSameRegime(t *testing.T) {
	input := completeInput([]evidence.GateEvidenceCell{
		cell(2, "moderate", "above-crossover", "tcp", PlacementMetric, 0.11),
		cell(4, "moderate", "above-crossover", "tcp", PlacementMetric, 0.12),
		cell(8, "moderate", "above-crossover", "tcp", PlacementMetric, 0.09),
		cell(16, "moderate", "above-crossover", "tcp", PlacementMetric, 0.13),
	})
	got, err := Decide(input)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if got.Branch != evidence.BranchPlacement {
		t.Fatalf("branch = %q, want placement", got.Branch)
	}
	want := []uint32{2, 4}
	if !sameWidths(got.QualifyingWidths, want) {
		t.Fatalf("qualifying widths = %v, want %v", got.QualifyingWidths, want)
	}
}

func TestNonAdjacentPlacementWidthsDoNotPass(t *testing.T) {
	input := completeInput([]evidence.GateEvidenceCell{
		cell(2, "moderate", "above-crossover", "tcp", PlacementMetric, 0.11),
		cell(4, "moderate", "above-crossover", "tcp", PlacementMetric, 0.09),
		cell(8, "moderate", "above-crossover", "tcp", PlacementMetric, 0.12),
		cell(16, "moderate", "above-crossover", "tcp", PlacementMetric, 0.09),
		cell(8, "moderate", "above-crossover", "tcp", SourcePressureMetric, 0.11),
	})
	got, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != evidence.BranchSourcePressure {
		t.Fatalf("branch = %q, want source-pressure", got.Branch)
	}
}

func TestCompleteEvidenceWithNoPassingGateProducesNegativeResult(t *testing.T) {
	input := completeInput([]evidence.GateEvidenceCell{
		cell(2, "moderate", "above-crossover", "tcp", PlacementMetric, 0.01),
		cell(4, "moderate", "above-crossover", "tcp", PlacementMetric, 0.02),
		cell(8, "moderate", "above-crossover", "tcp", PlacementMetric, 0.03),
		cell(16, "moderate", "above-crossover", "tcp", PlacementMetric, 0.04),
		cell(16, "moderate", "above-crossover", "tcp", SourcePressureMetric, 0.05),
	})
	got, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != evidence.BranchNegativeResult {
		t.Fatalf("branch = %q, want negative-result", got.Branch)
	}
}

func TestIncompleteEvidenceCannotBeSigned(t *testing.T) {
	input := completeInput(nil)
	input.EvidenceComplete = false
	decision, err := Decide(input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Branch != evidence.BranchInsufficientEvidence {
		t.Fatalf("branch = %q, want insufficient-evidence", decision.Branch)
	}
	private, _ := testKey()
	if _, err := Sign(decision, private); err == nil {
		t.Fatal("Sign() accepted insufficient evidence")
	}
}

func TestTamperedDecisionFailsVerification(t *testing.T) {
	decision, err := Decide(completeInput([]evidence.GateEvidenceCell{
		cell(2, "moderate", "above-crossover", "tcp", PlacementMetric, 0.11),
		cell(4, "moderate", "above-crossover", "tcp", PlacementMetric, 0.12),
	}))
	if err != nil {
		t.Fatal(err)
	}
	private, public := testKey()
	signed, err := Sign(decision, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signed, public); err != nil {
		t.Fatalf("Verify(original) error = %v", err)
	}
	signed.Decision.Threshold = 0.09
	if err := Verify(signed, public); err == nil {
		t.Fatal("Verify(tampered) error = nil")
	}
}

func completeInput(cells []evidence.GateEvidenceCell) DecisionInput {
	return DecisionInput{
		DecisionID:            "gate-test-1",
		PreregistrationSHA256: string(bytes.Repeat([]byte{'a'}, 64)),
		ArtifactLedgerSHA256:  string(bytes.Repeat([]byte{'b'}, 64)),
		EvidenceSHA256:        string(bytes.Repeat([]byte{'c'}, 64)),
		Threshold:             0.10,
		Confidence:            0.95,
		EvidenceComplete:      true,
		Evidence:              cells,
		DecidedAt:             time.Date(2026, 8, 25, 22, 0, 0, 0, time.UTC),
	}
}

func cell(width uint32, load evidence.LoadRegime, prefix, transport, metric string, lower float64) evidence.GateEvidenceCell {
	result := evidence.GateEvidenceCell{
		FanoutWidth:  width,
		LoadRegime:   load,
		PrefixRegime: prefix,
		Transport:    transport,
		Metric:       metric,
		Pairs:        20,
		Improvement:  evidence.ConfidenceInterval{Estimate: lower + 0.02, Lower: lower, Upper: lower + 0.04},
	}
	if metric == SourcePressureMetric {
		result.BaselineSourceCount = 1
		result.CandidateSourceCount = 2
	}
	return result
}

func testKey() (ed25519.PrivateKey, ed25519.PublicKey) {
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	private := ed25519.NewKeyFromSeed(seed)
	return private, private.Public().(ed25519.PublicKey)
}

func sameWidths(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
