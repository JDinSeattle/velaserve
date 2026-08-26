package gate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/evidence"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

type DecisionBindings struct {
	EvidencePath        string
	PreregistrationPath string
	LedgerPath          string
}

func GenerateKeyFiles(privatePath, publicPath string) error {
	if privatePath == publicPath {
		return fmt.Errorf("private and public key paths must differ")
	}
	for _, path := range []string{privatePath, publicPath} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("key path %q already exists", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect key path %q: %w", path, err)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate Ed25519 key: %w", err)
	}
	privateEncoded := base64.RawStdEncoding.EncodeToString(private) + "\n"
	publicEncoded := base64.RawStdEncoding.EncodeToString(public) + "\n"
	if err := writeExclusive(privatePath, []byte(privateEncoded), 0o600); err != nil {
		return err
	}
	if err := writeExclusive(publicPath, []byte(publicEncoded), 0o644); err != nil {
		_ = os.Remove(privatePath)
		return err
	}
	return nil
}

func SignDecisionFile(decisionPath, privatePath, outputPath string, bindings DecisionBindings) error {
	decision, err := decodeJSONFile[evidence.GateDecision](decisionPath)
	if err != nil {
		return fmt.Errorf("read gate decision: %w", err)
	}
	if err := verifyBindings(decision, bindings); err != nil {
		return err
	}
	privateBytes, err := readBase64Key(privatePath, ed25519.PrivateKeySize)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	signed, err := Sign(decision, ed25519.PrivateKey(privateBytes))
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return fmt.Errorf("encode signed gate decision: %w", err)
	}
	return writeExclusive(outputPath, append(encoded, '\n'), 0o644)
}

func VerifyDecisionFile(signedPath, publicPath string, bindings DecisionBindings) error {
	signed, err := decodeJSONFile[SignedDecision](signedPath)
	if err != nil {
		return fmt.Errorf("read signed gate decision: %w", err)
	}
	if err := verifyBindings(signed.Decision, bindings); err != nil {
		return err
	}
	publicBytes, err := readBase64Key(publicPath, ed25519.PublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	return Verify(signed, ed25519.PublicKey(publicBytes))
}

func verifyBindings(decision evidence.GateDecision, bindings DecisionBindings) error {
	want := []struct {
		name string
		path string
		hash string
	}{
		{"preregistration", bindings.PreregistrationPath, decision.PreregistrationSHA256},
		{"artifact ledger", bindings.LedgerPath, decision.ArtifactLedgerSHA256},
		{"gate evidence", bindings.EvidencePath, decision.EvidenceSHA256},
	}
	for _, binding := range want {
		got, err := hashPath(binding.path)
		if err != nil {
			return fmt.Errorf("hash bound %s: %w", binding.name, err)
		}
		if got != binding.hash {
			return fmt.Errorf("bound %s SHA-256 does not match decision", binding.name)
		}
	}
	cells, err := jsonl.Read[evidence.GateEvidenceCell](bindings.EvidencePath)
	if err != nil {
		return fmt.Errorf("read bound gate evidence: %w", err)
	}
	recomputed, err := Decide(DecisionInput{
		DecisionID:            decision.DecisionID,
		PreregistrationSHA256: decision.PreregistrationSHA256,
		ArtifactLedgerSHA256:  decision.ArtifactLedgerSHA256,
		EvidenceSHA256:        decision.EvidenceSHA256,
		Threshold:             decision.Threshold,
		Confidence:            decision.Confidence,
		EvidenceComplete:      decision.Branch != evidence.BranchInsufficientEvidence,
		Evidence:              cells,
		DecidedAt:             decision.DecidedAt,
	})
	if err != nil {
		return fmt.Errorf("recompute bound decision: %w", err)
	}
	if !decisionsEqual(decision, recomputed) {
		return fmt.Errorf("decision does not match bound gate evidence")
	}
	return nil
}

func decisionsEqual(left, right evidence.GateDecision) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func writeExclusive(path string, content []byte, mode os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create output %q without overwrite: %w", path, err)
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write output %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync output %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close output %q: %w", path, err)
	}
	success = true
	return nil
}

func decodeJSONFile[T any](path string) (T, error) {
	var zero T
	content, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return zero, fmt.Errorf("trailing JSON content")
	}
	return value, nil
}

func readBase64Key(path string, expectedBytes int) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(content)))
	if err != nil {
		return nil, err
	}
	if len(decoded) != expectedBytes {
		return nil, fmt.Errorf("decoded key has %d bytes, want %d", len(decoded), expectedBytes)
	}
	return decoded, nil
}

func hashPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
