package gate

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"

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
	if decision.Branch == evidence.BranchInsufficientEvidence {
		return SignedDecision{}, fmt.Errorf("cannot sign an insufficient-evidence decision")
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
	return nil
}

func canonicalDecision(decision evidence.GateDecision) ([]byte, error) {
	encoded, err := json.Marshal(decision)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical gate decision: %w", err)
	}
	return encoded, nil
}
