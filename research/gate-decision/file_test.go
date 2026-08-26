package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

func TestGateFileWorkflowGeneratesSignsAndVerifiesDecision(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "gate.private.key")
	publicPath := filepath.Join(root, "gate.public.key")
	evidencePath := filepath.Join(root, "cells.jsonl")
	preregistrationPath := filepath.Join(root, "z0-v1.yaml")
	ledgerPath := filepath.Join(root, "ledger.jsonl")
	decisionPath := filepath.Join(root, "decision.json")
	signedPath := filepath.Join(root, "decision.signed.json")

	if err := GenerateKeyFiles(privatePath, publicPath); err != nil {
		t.Fatalf("GenerateKeyFiles() error = %v", err)
	}
	info, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key permissions = %o, want 600", info.Mode().Perm())
	}
	for _, item := range []struct {
		path    string
		content string
	}{
		{preregistrationPath, "schema_version: velaserve.preregistration/v1\n"},
		{ledgerPath, "{\"relative_path\":\"raw/groups.jsonl\"}\n"},
	} {
		if err := os.WriteFile(item.path, []byte(item.content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := jsonl.Append(evidencePath, cell(2, "moderate", "above-crossover", "tcp", PlacementMetric, 0.11)); err != nil {
		t.Fatal(err)
	}
	if err := jsonl.Append(evidencePath, cell(4, "moderate", "above-crossover", "tcp", PlacementMetric, 0.12)); err != nil {
		t.Fatal(err)
	}

	preregistrationHash, _ := hashPath(preregistrationPath)
	ledgerHash, _ := hashPath(ledgerPath)
	evidenceHash, _ := hashPath(evidencePath)
	decision, err := Decide(DecisionInput{
		DecisionID: "gate-file-test", PreregistrationSHA256: preregistrationHash,
		ArtifactLedgerSHA256: ledgerHash, EvidenceSHA256: evidenceHash,
		Threshold: 0.10, Confidence: 0.95, EvidenceComplete: true,
		Evidence: []evidence.GateEvidenceCell{
			cell(2, "moderate", "above-crossover", "tcp", PlacementMetric, 0.11),
			cell(4, "moderate", "above-crossover", "tcp", PlacementMetric, 0.12),
		},
		DecidedAt: time.Date(2026, 8, 25, 22, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	decisionBytes, _ := json.MarshalIndent(decision, "", "  ")
	if err := os.WriteFile(decisionPath, append(decisionBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if decision.Branch != evidence.BranchPlacement {
		t.Fatalf("decision branch = %q, want placement", decision.Branch)
	}
	bindings := DecisionBindings{EvidencePath: evidencePath, PreregistrationPath: preregistrationPath, LedgerPath: ledgerPath}
	if err := SignDecisionFile(decisionPath, privatePath, signedPath, bindings); err != nil {
		t.Fatalf("SignDecisionFile() error = %v", err)
	}
	if err := VerifyDecisionFile(signedPath, publicPath, bindings); err != nil {
		t.Fatalf("VerifyDecisionFile() error = %v", err)
	}
	if err := os.WriteFile(evidencePath, append([]byte{}, '{', '}'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyDecisionFile(signedPath, publicPath, bindings); err == nil {
		t.Fatal("VerifyDecisionFile() accepted replaced evidence")
	}
}

func TestGenerateKeyFilesRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "gate.private.key")
	publicPath := filepath.Join(root, "gate.public.key")
	if err := GenerateKeyFiles(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	if err := GenerateKeyFiles(privatePath, publicPath); err == nil {
		t.Fatal("GenerateKeyFiles() overwrote existing keys")
	}
}
