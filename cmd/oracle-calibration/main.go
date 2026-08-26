package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/conditiondriver"
	"github.com/JDinSeattle/velaserve/research/crossover"
	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
	"go.yaml.in/yaml/v3"
)

func main() {
	profilePath := flag.String("profile", "", "generated benchmark profile")
	profileCalibrationPath := flag.String("profile-calibration", "", "tokenizer/profile calibration JSON")
	crossoverBundle := flag.String("crossover-bundle", "", "sealed raw crossover bundle")
	loadCalibrationPath := flag.String("load-calibration", "", "raw open-loop load calibration JSON")
	publicationDelay := flag.Float64("inflight-publication-delay-ms", 5, "deployed EPP inflight publication delay")
	affinityGate := flag.Float64("affinity-load-gate-seconds", 2, "deployed affinity-to-load gate")
	outputPath := flag.String("output", "", "new derived oracle calibration YAML")
	verifyPath := flag.String("verify", "", "verify an existing calibration instead of writing one")
	flag.Parse()
	if err := run(*profilePath, *profileCalibrationPath, *crossoverBundle, *loadCalibrationPath, *outputPath, *verifyPath, *publicationDelay, *affinityGate); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(profilePath, profileCalibrationPath, crossoverBundle, loadCalibrationPath, outputPath, verifyPath string, publicationDelay, affinityGate float64) error {
	if strings.TrimSpace(profilePath) == "" || strings.TrimSpace(profileCalibrationPath) == "" || strings.TrimSpace(crossoverBundle) == "" || strings.TrimSpace(loadCalibrationPath) == "" || (strings.TrimSpace(outputPath) == "") == (strings.TrimSpace(verifyPath) == "") {
		return fmt.Errorf("profile, profile calibration, crossover bundle, load calibration, and exactly one of output or verify are required")
	}
	profile, err := bench.LoadProfile(profilePath)
	if err != nil {
		return err
	}
	profileCalibration, profileSource, err := bench.LoadProfileCalibrationSource(profileCalibrationPath)
	if err != nil {
		return err
	}
	crossoverEvidence, err := crossover.VerifyBundle(crossoverBundle)
	if err != nil {
		return err
	}
	loadCalibration, err := readLoadCalibration(loadCalibrationPath)
	if err != nil {
		return err
	}
	derived, err := replay.DeriveReplayProfile(profile, profileCalibration, profileSource, crossoverEvidence, loadCalibration, publicationDelay, affinityGate)
	if err != nil {
		return err
	}
	encoded, err := yaml.Marshal(derived)
	if err != nil {
		return err
	}
	if verifyPath != "" {
		retained, err := os.ReadFile(verifyPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(retained, encoded) {
			return fmt.Errorf("oracle calibration differs from retained raw crossover/load observations")
		}
		fmt.Printf("oracle-calibration: verified %s\n", verifyPath)
		return nil
	}
	file, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(outputPath)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(outputPath)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(outputPath)
		return err
	}
	fmt.Printf("oracle-calibration: derived %s from sealed raw observations\n", outputPath)
	return nil
}

func readLoadCalibration(path string) (conditiondriver.LoadCalibration, error) {
	file, err := os.Open(path)
	if err != nil {
		return conditiondriver.LoadCalibration{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 32<<20))
	decoder.DisallowUnknownFields()
	var calibration conditiondriver.LoadCalibration
	if err := decoder.Decode(&calibration); err != nil {
		return conditiondriver.LoadCalibration{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return conditiondriver.LoadCalibration{}, fmt.Errorf("load calibration must contain exactly one JSON object")
	}
	return calibration, nil
}
