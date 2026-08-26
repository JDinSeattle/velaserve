package gate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const SignatureAlgorithm = "Ed25519"

type SignedDecision struct {
	SchemaVersion string                `json:"schema_version"`
	Algorithm     string                `json:"algorithm"`
	PublicKey     string                `json:"public_key"`
	Signature     string                `json:"signature"`
	Decision      evidence.GateDecision `json:"decision"`
}

func Sign(decision evidence.GateDecision, private ed25519.PrivateKey) (SignedDecision, error) {
	if err := ValidateDecision(decision); err != nil {
		return SignedDecision{}, fmt.Errorf("refuse invalid decision: %w", err)
	}
	if len(private) != ed25519.PrivateKeySize {
		return SignedDecision{}, fmt.Errorf("invalid Ed25519 private key length %d", len(private))
	}
	canonical, err := canonicalDecision(decision)
	if err != nil {
		return SignedDecision{}, err
	}
	public := private.Public().(ed25519.PublicKey)
	return SignedDecision{
		SchemaVersion: "velaserve.signed-gate/v1",
		Algorithm:     SignatureAlgorithm,
		PublicKey:     base64.RawStdEncoding.EncodeToString(public),
		Signature:     base64.RawStdEncoding.EncodeToString(ed25519.Sign(private, canonical)),
		Decision:      decision,
	}, nil
}

func Verify(signed SignedDecision, expectedPublic ed25519.PublicKey) error {
	if signed.SchemaVersion != "velaserve.signed-gate/v1" || signed.Algorithm != SignatureAlgorithm {
		return fmt.Errorf("unsupported signed gate envelope")
	}
	embeddedPublic, err := base64.RawStdEncoding.DecodeString(signed.PublicKey)
	if err != nil {
		return fmt.Errorf("decode embedded public key: %w", err)
	}
	if len(expectedPublic) != ed25519.PublicKeySize || !bytes.Equal(embeddedPublic, expectedPublic) {
		return fmt.Errorf("embedded public key does not match expected key")
	}
	signature, err := base64.RawStdEncoding.DecodeString(signed.Signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	canonical, err := canonicalDecision(signed.Decision)
	if err != nil {
		return err
	}
	if !ed25519.Verify(expectedPublic, canonical, signature) {
		return fmt.Errorf("gate decision signature verification failed")
	}
	return ValidateDecision(signed.Decision)
}

// ValidateDecision replays the deterministic gate from the evidence embedded
// in the decision. A signature therefore cannot bless a caller-selected branch
// or qualifying-width list, even when the JSON is otherwise well formed.
func ValidateDecision(decision evidence.GateDecision) error {
	if decision.SchemaVersion != evidence.SchemaVersion {
		return fmt.Errorf("unsupported decision schema %q", decision.SchemaVersion)
	}
	if decision.Branch == evidence.BranchInsufficientEvidence {
		return fmt.Errorf("cannot sign an insufficient-evidence decision")
	}
	recomputed, err := Decide(DecisionInput{
		DecisionID:            decision.DecisionID,
		PreregistrationSHA256: decision.PreregistrationSHA256,
		ArtifactLedgerSHA256:  decision.ArtifactLedgerSHA256,
		EvidenceSHA256:        decision.EvidenceSHA256,
		Threshold:             decision.Threshold,
		Confidence:            decision.Confidence,
		EvidenceComplete:      true,
		Evidence:              decision.Evidence,
		DecidedAt:             decision.DecidedAt,
	})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(decision, recomputed) {
		return fmt.Errorf("decision branch, widths, ordering, or evidence do not match deterministic recomputation")
	}
	return nil
}

func canonicalDecision(decision evidence.GateDecision) ([]byte, error) {
	encoded, err := json.Marshal(decision)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical gate decision: %w", err)
	}
	return encoded, nil
}
