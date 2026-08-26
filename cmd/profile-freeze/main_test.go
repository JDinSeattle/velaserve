package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
)

func TestGenerateBindsMeasuredThresholdIntoAllOutputs(t *testing.T) {
	const model = "test/model"
	revision := strings.Repeat("a", 40)
	evidencePath := filepath.Join(t.TempDir(), "crossover.json")
	value := crossoverEvidenceFixture(model, revision)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"tokenizer_class":"FixtureTokenizer","chat_template":"fixture-template"}`))
			return
		}
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || len(payload.Messages) != 1 {
			http.Error(writer, "bad payload", http.StatusBadRequest)
			return
		}
		content := payload.Messages[0].Content
		prefix := strings.SplitN(content, "\n", 2)[0]
		count := strings.Count(prefix, "shared zeroing context token ")
		tokens := make([]uint32, count+1)
		for index := 0; index < count; index++ {
			tokens[index] = uint32(index + 1)
		}
		tokens[count] = uint32(len(content) + 10_000)
		_ = json.NewEncoder(writer).Encode(map[string]any{"count": len(tokens), "max_model_len": 32768, "tokens": tokens})
	}))
	defer server.Close()
	root := t.TempDir()
	profilePath := filepath.Join(root, "profile.yaml")
	calibrationPath := filepath.Join(root, "profile-calibration.json")
	routerValuesPath := filepath.Join(root, "router-values.yaml")
	err = generate([]string{
		"--template", "../../benchmarks/profiles/aws-z0-template.yaml", "--crossover-evidence", evidencePath,
		"--tokenize-url", server.URL + "/tokenize", "--tokenizer-info-url", server.URL + "/get_tokenizer_info",
		"--model", model, "--revision", revision, "--phase", "z0-b", "--arm", "arm-b-load-aware-p2p", "--epp-replicas", "1", "--transport", "tcp",
		"--output-profile", profilePath, "--output-calibration", calibrationPath, "--output-router-values", routerValuesPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := bench.LoadProfile(profilePath)
	if err != nil {
		contents, _ := os.ReadFile(profilePath)
		t.Fatalf("%v\n%s", err, contents)
	}
	calibration, err := bench.LoadProfileCalibration(calibrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := bench.ValidateProfileCalibration(profile, calibration); err != nil {
		t.Fatal(err)
	}
	if calibration.MinCachedTokenDelta != 128 || calibration.CrossoverEvidenceSHA256 == "" {
		t.Fatalf("calibration threshold/hash = %d/%q", calibration.MinCachedTokenDelta, calibration.CrossoverEvidenceSHA256)
	}
	routerValues, err := os.ReadFile(routerValuesPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(routerValues) != "model:\n  minCachedTokenDelta: 128\n" {
		t.Fatalf("router values = %q", routerValues)
	}
}

func crossoverEvidenceFixture(model, revision string) bench.CrossoverEvidence {
	now := time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)
	trials := make([]bench.CrossoverTrial, 0, 18)
	for _, prefix := range []uint64{64, 128, 256} {
		for index := 0; index < 3; index++ {
			trials = append(trials,
				bench.CrossoverTrial{RunID: "run", GroupID: fmt.Sprintf("r-%d-%d", prefix, index), RequestID: fmt.Sprintf("rr-%d-%d", prefix, index), PrefixTokens: prefix, Acquisition: "recompute", Seconds: float64(prefix) / 1000, ObservedAt: now.Add(time.Duration(len(trials)) * time.Second)},
				bench.CrossoverTrial{RunID: "run", GroupID: fmt.Sprintf("p-%d-%d", prefix, index), RequestID: fmt.Sprintf("pp-%d-%d", prefix, index), PrefixTokens: prefix, Acquisition: "p2p", Seconds: map[uint64]float64{64: .1, 128: .08, 256: .1}[prefix], TransferBytes: prefix * 512, ObservedAt: now.Add(time.Duration(len(trials)+1) * time.Second)},
			)
		}
	}
	samples, err := bench.AggregateCrossoverTrials(trials)
	if err != nil {
		panic(err)
	}
	return bench.CrossoverEvidence{
		SchemaVersion: bench.CrossoverEvidenceSchemaVersion, ModelID: model, ModelRevision: revision, Transport: "tcp",
		Deployment: bench.CrossoverDeploymentBinding{
			RepositoryCommit: strings.Repeat("b", 40), ModelImage: "example/model@sha256:" + strings.Repeat("c", 64), ModelSpecSHA256: strings.Repeat("d", 64), ActiveArm: "arm-b-load-aware-p2p",
			EPPImage: "example/epp@sha256:" + strings.Repeat("e", 64), EPPReplicas: 1, ReplicaCount: 6, GPUModel: "L40S", GPUDriverVersion: "570", InstanceType: "g6e.xlarge",
			RouterConfigInvariantSHA256: strings.Repeat("1", 64),
		},
		ObservationsSHA256: strings.Repeat("2", 64), RuntimeRawSHA256: strings.Repeat("3", 64), CalibrationBindingSHA256: strings.Repeat("4", 64),
		Trials: trials, Samples: samples, MinCachedTokenDelta: 128,
	}
}
