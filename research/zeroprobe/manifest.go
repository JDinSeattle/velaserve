package zeroprobe

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/artifacts"
	"github.com/JDinSeattle/velaserve/internal/evidence"
	"go.yaml.in/yaml/v3"
)

const (
	ManifestSchemaVersion  = "velaserve.run-manifest/v1"
	maxPreregistrationSize = 1 << 20
)

type Phase string

const (
	PhaseZ0A Phase = "z0-a"
	PhaseZ0B Phase = "z0-b"
	PhaseZ0C Phase = "z0-c"
)

type Manifest struct {
	SchemaVersion         string            `json:"schema_version"`
	RunID                 string            `json:"run_id"`
	Phase                 Phase             `json:"phase"`
	Seed                  uint64            `json:"seed"`
	PreregistrationSHA256 string            `json:"preregistration_sha256"`
	Widths                []uint32          `json:"widths"`
	ArrivalSkewsMS        []uint32          `json:"arrival_skews_ms"`
	EPPReplicas           []uint32          `json:"epp_replicas"`
	Arms                  []evidence.Arm    `json:"arms"`
	Upstream              map[string]string `json:"upstream"`
	CreatedAt             time.Time         `json:"created_at"`
}

type PrepareOptions struct {
	Phase               Phase
	PreregistrationPath string
	ArtifactRoot        string
	CreatedAt           time.Time
}

type preregistration struct {
	SchemaVersion string `yaml:"schema_version"`
	Seed          uint64 `yaml:"seed"`
	Z0A           struct {
		FanoutWidths []uint32 `yaml:"fanout_widths"`
		EPPReplicas  []uint32 `yaml:"epp_replicas"`
		ArrivalSkews []uint32 `yaml:"arrival_skew_ms"`
	} `yaml:"z0_a"`
	Arms struct {
		Primary []struct {
			ID evidence.Arm `yaml:"id"`
		} `yaml:"primary"`
		Controls []struct {
			ID evidence.Arm `yaml:"id"`
		} `yaml:"controls"`
	} `yaml:"arms"`
	Upstream map[string]string `yaml:"upstream"`
}

func Prepare(options PrepareOptions) (Manifest, error) {
	if options.Phase != PhaseZ0A && options.Phase != PhaseZ0B && options.Phase != PhaseZ0C {
		return Manifest{}, fmt.Errorf("phase: unsupported value %q", options.Phase)
	}
	if strings.TrimSpace(options.PreregistrationPath) == "" || strings.TrimSpace(options.ArtifactRoot) == "" {
		return Manifest{}, fmt.Errorf("preregistration path and artifact root are required")
	}
	contents, err := readBoundedFile(options.PreregistrationPath, maxPreregistrationSize)
	if err != nil {
		return Manifest{}, fmt.Errorf("read preregistration: %w", err)
	}
	var registered preregistration
	if err := yaml.Unmarshal(contents, &registered); err != nil {
		return Manifest{}, fmt.Errorf("decode preregistration: %w", err)
	}
	if err := validatePreregistration(registered); err != nil {
		return Manifest{}, err
	}
	createdAt := options.CreatedAt.UTC()
	if options.CreatedAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	root, err := filepath.Abs(options.ArtifactRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create artifact root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve artifact root symlinks: %w", err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if _, err := os.Lstat(manifestPath); err == nil {
		return Manifest{}, fmt.Errorf("manifest already exists")
	} else if !os.IsNotExist(err) {
		return Manifest{}, fmt.Errorf("inspect manifest: %w", err)
	}
	runID, err := randomRunID()
	if err != nil {
		return Manifest{}, err
	}
	preregistrationHash := sha256.Sum256(contents)
	arms := make([]evidence.Arm, 0, len(registered.Arms.Primary)+len(registered.Arms.Controls))
	for _, arm := range registered.Arms.Primary {
		arms = append(arms, arm.ID)
	}
	for _, arm := range registered.Arms.Controls {
		arms = append(arms, arm.ID)
	}
	manifest := Manifest{
		SchemaVersion:         ManifestSchemaVersion,
		RunID:                 runID,
		Phase:                 options.Phase,
		Seed:                  registered.Seed,
		PreregistrationSHA256: hex.EncodeToString(preregistrationHash[:]),
		Widths:                append([]uint32(nil), registered.Z0A.FanoutWidths...),
		ArrivalSkewsMS:        append([]uint32(nil), registered.Z0A.ArrivalSkews...),
		EPPReplicas:           append([]uint32(nil), registered.Z0A.EPPReplicas...),
		Arms:                  arms,
		Upstream:              cloneMap(registered.Upstream),
		CreatedAt:             createdAt,
	}
	if err := writeExclusive(filepath.Join(root, "preregistration.yaml"), contents); err != nil {
		return Manifest{}, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("encode manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeExclusive(manifestPath, encoded); err != nil {
		return Manifest{}, err
	}
	for _, relative := range []string{"preregistration.yaml", "manifest.json"} {
		if _, err := artifacts.Record(root, relative); err != nil {
			return Manifest{}, fmt.Errorf("record %s: %w", relative, err)
		}
	}
	return manifest, nil
}

func validatePreregistration(registered preregistration) error {
	if registered.SchemaVersion != "velaserve.preregistration/v1" {
		return fmt.Errorf("preregistration schema_version is unsupported")
	}
	if registered.Seed != 20260825 {
		return fmt.Errorf("preregistration seed: got %d, want frozen value 20260825", registered.Seed)
	}
	if !sameUint32s(registered.Z0A.FanoutWidths, []uint32{2, 4, 8, 16}) ||
		!sameUint32s(registered.Z0A.ArrivalSkews, []uint32{0, 1, 5, 20}) ||
		!sameUint32s(registered.Z0A.EPPReplicas, []uint32{1, 2}) {
		return fmt.Errorf("preregistration Z0-A matrix differs from the frozen contract")
	}
	if len(registered.Arms.Primary) < 2 || len(registered.Upstream) != 4 {
		return fmt.Errorf("preregistration arms or upstream pins are incomplete")
	}
	for name, revision := range registered.Upstream {
		if strings.TrimSpace(name) == "" || len(revision) != 40 {
			return fmt.Errorf("upstream revision %q is not a full commit SHA", name)
		}
	}
	return nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return contents, nil
}

func writeExclusive(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := io.Copy(file, bytes.NewReader(contents)); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func randomRunID() (string, error) {
	var entropy [16]byte
	if _, err := io.ReadFull(rand.Reader, entropy[:]); err != nil {
		return "", fmt.Errorf("generate run ID: %w", err)
	}
	return "z0-" + hex.EncodeToString(entropy[:]), nil
}

func cloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func sameUint32s(left, right []uint32) bool {
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
