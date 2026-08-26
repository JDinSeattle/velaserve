package gate

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const (
	PlacementMetric      = "p95_group_makespan_seconds"
	SourcePressureMetric = "p95_last_sibling_ttft_seconds"
)

var registeredWidths = []uint32{2, 4, 8, 16}

type DecisionInput struct {
	DecisionID            string
	PreregistrationSHA256 string
	ArtifactLedgerSHA256  string
	Threshold             float64
	Confidence            float64
	EvidenceComplete      bool
	Evidence              []evidence.GateEvidenceCell
	DecidedAt             time.Time
}

func Decide(input DecisionInput) (evidence.GateDecision, error) {
	if err := validateDecisionInput(input); err != nil {
		return evidence.GateDecision{}, err
	}
	cells := append([]evidence.GateEvidenceCell(nil), input.Evidence...)
	sort.Slice(cells, func(i, j int) bool { return cellLess(cells[i], cells[j]) })
	decision := evidence.GateDecision{
		SchemaVersion:         evidence.SchemaVersion,
		DecisionID:            input.DecisionID,
		PreregistrationSHA256: input.PreregistrationSHA256,
		ArtifactLedgerSHA256:  input.ArtifactLedgerSHA256,
		Threshold:             input.Threshold,
		Confidence:            input.Confidence,
		Evidence:              cells,
		DecidedAt:             input.DecidedAt.UTC(),
	}
	if !input.EvidenceComplete {
		decision.Branch = evidence.BranchInsufficientEvidence
		return decision, nil
	}
	if widths := placementQualifyingWidths(cells, input.Threshold); len(widths) >= 2 {
		decision.Branch = evidence.BranchPlacement
		decision.QualifyingWidths = widths
		return decision, nil
	}
	if widths := sourcePressureQualifyingWidths(cells, input.Threshold); len(widths) > 0 {
		decision.Branch = evidence.BranchSourcePressure
		decision.QualifyingWidths = widths
		return decision, nil
	}
	decision.Branch = evidence.BranchNegativeResult
	return decision, nil
}

func placementQualifyingWidths(cells []evidence.GateEvidenceCell, threshold float64) []uint32 {
	byRegime := make(map[string]map[uint32]bool)
	for _, cell := range cells {
		if cell.Metric != PlacementMetric || cell.Pairs < 20 {
			continue
		}
		key := regimeKey(cell)
		if byRegime[key] == nil {
			byRegime[key] = make(map[uint32]bool)
		}
		byRegime[key][cell.FanoutWidth] = cell.Improvement.Lower >= threshold
	}
	keys := make([]string, 0, len(byRegime))
	for key := range byRegime {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		passing := byRegime[key]
		for index := 0; index < len(registeredWidths)-1; index++ {
			left := registeredWidths[index]
			right := registeredWidths[index+1]
			if passing[left] && passing[right] {
				result := []uint32{left, right}
				for next := index + 2; next < len(registeredWidths) && passing[registeredWidths[next]]; next++ {
					result = append(result, registeredWidths[next])
				}
				return result
			}
		}
	}
	return nil
}

func sourcePressureQualifyingWidths(cells []evidence.GateEvidenceCell, threshold float64) []uint32 {
	seen := make(map[uint32]struct{})
	for _, cell := range cells {
		if cell.Metric == SourcePressureMetric && cell.Pairs >= 20 && cell.Improvement.Lower >= threshold {
			seen[cell.FanoutWidth] = struct{}{}
		}
	}
	result := make([]uint32, 0, len(seen))
	for _, width := range registeredWidths {
		if _, ok := seen[width]; ok {
			result = append(result, width)
		}
	}
	return result
}

func validateDecisionInput(input DecisionInput) error {
	if strings.TrimSpace(input.DecisionID) == "" {
		return fmt.Errorf("decision ID is required")
	}
	if !isLowerHexSHA256(input.PreregistrationSHA256) {
		return fmt.Errorf("preregistration SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if !isLowerHexSHA256(input.ArtifactLedgerSHA256) {
		return fmt.Errorf("artifact ledger SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if math.Abs(input.Threshold-0.10) > 1e-12 {
		return fmt.Errorf("threshold %.6f does not match preregistered 0.10", input.Threshold)
	}
	if math.Abs(input.Confidence-0.95) > 1e-12 {
		return fmt.Errorf("confidence %.6f does not match preregistered 0.95", input.Confidence)
	}
	if input.DecidedAt.IsZero() {
		return fmt.Errorf("decision time is required")
	}
	for index, cell := range input.Evidence {
		if !registeredWidth(cell.FanoutWidth) {
			return fmt.Errorf("evidence[%d] has unregistered width %d", index, cell.FanoutWidth)
		}
		if cell.Pairs < 20 {
			return fmt.Errorf("evidence[%d] has %d pairs; minimum is 20", index, cell.Pairs)
		}
		if strings.TrimSpace(cell.PrefixRegime) == "" || strings.TrimSpace(cell.Transport) == "" {
			return fmt.Errorf("evidence[%d] requires prefix regime and transport", index)
		}
		if cell.Metric != PlacementMetric && cell.Metric != SourcePressureMetric {
			return fmt.Errorf("evidence[%d] has unsupported metric %q", index, cell.Metric)
		}
		interval := cell.Improvement
		if nonFinite(interval.Estimate) || nonFinite(interval.Lower) || nonFinite(interval.Upper) || interval.Lower > interval.Estimate || interval.Estimate > interval.Upper {
			return fmt.Errorf("evidence[%d] has invalid confidence interval", index)
		}
	}
	return nil
}

func regimeKey(cell evidence.GateEvidenceCell) string {
	return string(cell.LoadRegime) + "\x00" + cell.PrefixRegime + "\x00" + cell.Transport
}

func cellLess(left, right evidence.GateEvidenceCell) bool {
	leftKey := regimeKey(left) + "\x00" + left.Metric
	rightKey := regimeKey(right) + "\x00" + right.Metric
	if leftKey != rightKey {
		return leftKey < rightKey
	}
	return left.FanoutWidth < right.FanoutWidth
}

func registeredWidth(width uint32) bool {
	for _, registered := range registeredWidths {
		if width == registered {
			return true
		}
	}
	return false
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func nonFinite(value float64) bool { return math.IsNaN(value) || math.IsInf(value, 0) }
