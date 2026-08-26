package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/conditiondriver"
	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const maxAuthorizationBytes = 64 << 10
const maxEndpointsBytes = 1 << 20

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("load-calibration", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "", "OpenAI-compatible gateway /v1/chat/completions URL")
	gatewayChatURL := flags.String("gateway-chat-url", "", "stable in-cluster gateway URL retained in deployment evidence (defaults to endpoint)")
	authorizationFile := flags.String("authorization-file", "", "optional file containing the Authorization header value")
	profilePath := flags.String("profile", "", "tokenizer-calibrated AWS Z0 benchmark profile")
	profileCalibrationPath := flags.String("profile-calibration", "", "exact profile calibration JSON")
	phase := flags.String("phase", "", "z0-a, z0-b, or z0-c")
	modelImage := flags.String("model-image", "", "digest-addressed model image")
	instanceType := flags.String("instance-type", "", "homogeneous GPU instance type")
	gpuModel := flags.String("gpu-model", "", "observed GPU model")
	gpuDriverVersion := flags.String("gpu-driver-version", "", "observed GPU driver version")
	replicaCount := flags.Uint("replica-count", 0, "ready homogeneous model replica count")
	transport := flags.String("transport", "", "tcp or efa")
	activeArm := flags.String("active-arm", "", "exact deployed routing arm")
	routingSHA256 := flags.String("routing-sha256", "", "live routing object binding SHA-256")
	routerConfigSHA256 := flags.String("router-config-sha256", "", "live router ConfigMap binding SHA-256")
	endpointsPath := flags.String("endpoints", "", "exact model endpoint JSON with pod UID and direct metrics URL")
	metricsAccessPath := flags.String("metrics-access", "", "optional JSON object mapping endpoint IDs to operator-local metrics tunnel URLs")
	rates := flags.String("rates", "", "comma-separated strictly increasing offered QPS ramp")
	warmupDuration := flags.Duration("warmup-duration", 5*time.Second, "separate warmup window before every measured trial")
	duration := flags.Duration("trial-duration", 30*time.Second, "measurement window for every shape/rate trial")
	requestTimeout := flags.Duration("request-timeout", 180*time.Second, "per-request timeout, bounded by the trial window")
	drainTimeout := flags.Duration("drain-timeout", 2*time.Minute, "maximum time to prove every exact replica idle before/after a window")
	drainPollInterval := flags.Duration("drain-poll-interval", 500*time.Millisecond, "interval between exact-replica running/waiting gauge samples")
	maxInflight := flags.Int("max-inflight", 512, "maximum concurrently executing calibration requests")
	calibrationOutput := flags.String("output-calibration", "", "new raw load-calibration JSON path")
	profilesOutput := flags.String("output-profiles", "", "new derived load-profiles JSON path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || anyBlank(*endpoint, *profilePath, *profileCalibrationPath, *phase, *modelImage, *instanceType, *gpuModel, *gpuDriverVersion, *transport, *activeArm, *routingSHA256, *routerConfigSHA256, *endpointsPath, *rates, *calibrationOutput, *profilesOutput) || *replicaCount == 0 || *replicaCount > math.MaxUint32 {
		return fmt.Errorf("endpoint, profile, profile-calibration, phase, deployment/routing identity, rates, replica-count, and both output paths are required")
	}
	if filepath.Clean(*calibrationOutput) == filepath.Clean(*profilesOutput) {
		return fmt.Errorf("calibration and profile outputs must be different paths")
	}
	for _, path := range []string{*calibrationOutput, *profilesOutput} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("output already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect output %s: %w", path, err)
		}
	}
	profile, err := bench.LoadProfile(*profilePath)
	if err != nil {
		return err
	}
	profileCalibration, profileCalibrationSource, err := bench.LoadProfileCalibrationSource(*profileCalibrationPath)
	if err != nil {
		return err
	}
	if err := bench.ValidateAWSZ0Profile(profile, *phase); err != nil {
		return err
	}
	if err := bench.ValidateProfileCalibration(profile, profileCalibration); err != nil {
		return err
	}
	if profileCalibration.Transport != *transport {
		return fmt.Errorf("requested transport does not match the profile calibration")
	}
	profileCalibrationDigest := sha256.Sum256(profileCalibrationSource)
	parsedRates, err := parseRates(*rates)
	if err != nil {
		return err
	}
	authorization, err := readOptionalAuthorization(*authorizationFile)
	if err != nil {
		return err
	}
	endpoints, err := readCalibrationEndpoints(*endpointsPath)
	if err != nil {
		return err
	}
	metricsAccess, err := readMetricsAccess(*metricsAccessPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	calibration, collectErr := conditiondriver.CollectLoadCalibration(ctx, conditiondriver.LoadCalibrationCollectionOptions{
		Endpoint: *endpoint, GatewayChatURL: *gatewayChatURL, Authorization: authorization, Profile: profile,
		ProfileCalibrationSHA256: hex.EncodeToString(profileCalibrationDigest[:]), ModelRevision: profileCalibration.ModelRevision,
		ModelImage: *modelImage, InstanceType: *instanceType, GPUModel: *gpuModel, GPUDriverVersion: *gpuDriverVersion,
		ReplicaCount: uint32(*replicaCount), Transport: *transport, RatesQPS: parsedRates, TrialDuration: *duration,
		ActiveArm: evidence.Arm(*activeArm), RoutingSHA256: *routingSHA256, RouterConfigSHA256: *routerConfigSHA256,
		Endpoints: endpoints, MetricsAccessURLs: metricsAccess, WarmupDuration: *warmupDuration, RequestTimeout: *requestTimeout,
		DrainTimeout: *drainTimeout, DrainPollInterval: *drainPollInterval, MaxInflight: *maxInflight, HTTPClient: &http.Client{},
	})
	if len(calibration.Trials) == 0 {
		if collectErr != nil {
			return collectErr
		}
		return fmt.Errorf("load calibration produced no trials")
	}
	calibrationBytes, err := json.MarshalIndent(calibration, "", "  ")
	if err != nil {
		return err
	}
	if err := writeExclusiveAtomic(*calibrationOutput, append(calibrationBytes, '\n')); err != nil {
		return err
	}
	if collectErr != nil {
		return fmt.Errorf("raw calibration retained at %s, but collection did not complete: %w", *calibrationOutput, collectErr)
	}
	profiles, err := conditiondriver.DeriveLoadProfiles(calibration)
	if err != nil {
		return fmt.Errorf("raw calibration retained at %s, but it is not gate-eligible: %w", *calibrationOutput, err)
	}
	profilesBytes, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	if err := writeExclusiveAtomic(*profilesOutput, append(profilesBytes, '\n')); err != nil {
		return err
	}
	digest, err := conditiondriver.LoadCalibrationSHA256(calibration)
	if err != nil {
		return err
	}
	fmt.Printf("load-calibration: retained %d raw trials and %d derived profiles bound to %s\n", len(calibration.Trials), len(profiles), digest)
	return nil
}

func readMetricsAccess(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("metrics access file must be regular and not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxEndpointsBytes+1))
	var result map[string]string
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode metrics access URLs: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode metrics access URLs: trailing JSON content")
	}
	return result, nil
}

func readCalibrationEndpoints(path string) ([]conditiondriver.LoadCalibrationEndpoint, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("endpoints path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("endpoints file must be regular and not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxEndpointsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maxEndpointsBytes {
		return nil, fmt.Errorf("endpoints file exceeds %d bytes", maxEndpointsBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var endpoints []conditiondriver.LoadCalibrationEndpoint
	if err := decoder.Decode(&endpoints); err != nil {
		return nil, fmt.Errorf("decode endpoints: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode endpoints: trailing JSON content")
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("endpoints file is empty")
	}
	return endpoints, nil
}

func parseRates(value string) ([]float64, error) {
	parts := strings.Split(value, ",")
	rates := make([]float64, 0, len(parts))
	previous := 0.0
	for index, part := range parts {
		rate, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 || rate <= previous {
			return nil, fmt.Errorf("rate %d must be a finite positive value strictly above its predecessor", index+1)
		}
		rates = append(rates, rate)
		previous = rate
	}
	return rates, nil
}

func readOptionalAuthorization(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("authorization file must be regular and not a symlink")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxAuthorizationBytes+1))
	if err != nil {
		return "", err
	}
	if len(contents) > maxAuthorizationBytes {
		return "", fmt.Errorf("authorization file exceeds %d bytes", maxAuthorizationBytes)
	}
	return strings.TrimSpace(string(contents)), nil
}

func writeExclusiveAtomic(path string, contents []byte) (err error) {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary output for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("publish output %s without overwrite: %w", path, err)
	}
	return nil
}

func anyBlank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
