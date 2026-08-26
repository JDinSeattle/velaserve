package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/conditiondriver"
)

const maxConfigBytes = 1 << 20

func main() {
	if len(os.Args) == 5 && os.Args[1] == "validate-load-calibration" {
		calibration, err := decodeLoadCalibration(os.Args[2])
		if err == nil {
			var profiles []conditiondriver.LoadProfile
			profiles, err = decodeLoadProfiles(os.Args[3])
			if err == nil {
				_, err = conditiondriver.ValidateLoadCalibration(calibration, profiles, os.Args[4])
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "hash-load-profiles" {
		profiles, err := decodeLoadProfiles(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		digest, err := conditiondriver.LoadProfilesSHA256(profiles)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(digest)
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "hash-load-calibration" {
		calibration, err := decodeLoadCalibration(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		digest, err := conditiondriver.LoadCalibrationSHA256(calibration)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(digest)
		return
	}
	config, listen, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	driver, err := conditiondriver.New(config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer driver.Close()
	controlToken := strings.TrimSpace(os.Getenv("VELASERVE_CONDITION_CONTROL_TOKEN"))
	if controlToken == "" {
		fmt.Fprintln(os.Stderr, "VELASERVE_CONDITION_CONTROL_TOKEN is required")
		os.Exit(2)
	}
	server := &http.Server{
		Addr: listen, Handler: conditiondriver.Handler{Driver: driver, ControlToken: controlToken},
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadConfig() (conditiondriver.Config, string, error) {
	listen := envOrDefault("VELASERVE_DRIVER_LISTEN", ":8083")
	endpointsPath := strings.TrimSpace(os.Getenv("VELASERVE_DRIVER_ENDPOINTS_FILE"))
	if endpointsPath == "" {
		return conditiondriver.Config{}, "", fmt.Errorf("VELASERVE_DRIVER_ENDPOINTS_FILE is required")
	}
	file, err := os.Open(endpointsPath)
	if err != nil {
		return conditiondriver.Config{}, "", fmt.Errorf("open endpoints file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes+1))
	decoder.DisallowUnknownFields()
	var endpoints []conditiondriver.Endpoint
	if err := decoder.Decode(&endpoints); err != nil {
		return conditiondriver.Config{}, "", fmt.Errorf("decode endpoints file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return conditiondriver.Config{}, "", fmt.Errorf("endpoints file must contain exactly one JSON array")
	}

	loadProfilesPath := strings.TrimSpace(os.Getenv("VELASERVE_DRIVER_LOAD_PROFILES_FILE"))
	if loadProfilesPath == "" {
		return conditiondriver.Config{}, "", fmt.Errorf("VELASERVE_DRIVER_LOAD_PROFILES_FILE is required")
	}
	loadProfiles, err := decodeLoadProfiles(loadProfilesPath)
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	loadCalibrationPath := strings.TrimSpace(os.Getenv("VELASERVE_DRIVER_LOAD_CALIBRATION_FILE"))
	if loadCalibrationPath == "" {
		return conditiondriver.Config{}, "", fmt.Errorf("VELASERVE_DRIVER_LOAD_CALIBRATION_FILE is required")
	}
	loadCalibration, err := decodeLoadCalibration(loadCalibrationPath)
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	stabilizationSeconds, err := positiveFloatEnv("VELASERVE_DRIVER_STABILIZATION_SECONDS")
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	requestTimeoutSeconds, err := positiveFloatEnv("VELASERVE_DRIVER_REQUEST_TIMEOUT_SECONDS")
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	drainTimeoutSeconds, err := positiveFloatEnv("VELASERVE_DRIVER_DRAIN_TIMEOUT_SECONDS")
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	drainPollMilliseconds, err := positiveFloatEnv("VELASERVE_DRIVER_DRAIN_POLL_MILLISECONDS")
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	if drainTimeoutSeconds < 30 || drainTimeoutSeconds > 600 || drainPollMilliseconds < 50 || drainPollMilliseconds > 5000 || drainPollMilliseconds >= drainTimeoutSeconds*1000 {
		return conditiondriver.Config{}, "", fmt.Errorf("driver drain timeout must be 30..600 seconds and poll interval 50..5000 ms below the timeout")
	}
	authorization, err := optionalSecretFile("VELASERVE_DRIVER_AUTHORIZATION_FILE")
	if err != nil {
		return conditiondriver.Config{}, "", err
	}
	return conditiondriver.Config{
		Model:          strings.TrimSpace(os.Getenv("VELASERVE_DRIVER_MODEL")),
		GatewayChatURL: strings.TrimSpace(os.Getenv("VELASERVE_DRIVER_GATEWAY_CHAT_URL")),
		LoadProfiles:   loadProfiles, LoadCalibration: loadCalibration, Stabilization: time.Duration(stabilizationSeconds * float64(time.Second)),
		RequestTimeout: time.Duration(requestTimeoutSeconds * float64(time.Second)), Authorization: authorization,
		DrainTimeout: time.Duration(drainTimeoutSeconds * float64(time.Second)), DrainPollInterval: time.Duration(drainPollMilliseconds * float64(time.Millisecond)),
		Endpoints: endpoints,
	}, listen, nil
}

func decodeLoadCalibration(path string) (conditiondriver.LoadCalibration, error) {
	file, err := os.Open(path)
	if err != nil {
		return conditiondriver.LoadCalibration{}, fmt.Errorf("open load calibration file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes+1))
	decoder.DisallowUnknownFields()
	var calibration conditiondriver.LoadCalibration
	if err := decoder.Decode(&calibration); err != nil {
		return conditiondriver.LoadCalibration{}, fmt.Errorf("decode load calibration file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return conditiondriver.LoadCalibration{}, fmt.Errorf("load calibration file must contain exactly one JSON object")
	}
	return calibration, nil
}

func decodeLoadProfiles(path string) ([]conditiondriver.LoadProfile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open load profiles file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes+1))
	decoder.DisallowUnknownFields()
	var profiles []conditiondriver.LoadProfile
	if err := decoder.Decode(&profiles); err != nil {
		return nil, fmt.Errorf("decode load profiles file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("load profiles file must contain exactly one JSON array")
	}
	return profiles, nil
}

func positiveFloatEnv(name string) (float64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", name)
	}
	return parsed, nil
}

func optionalSecretFile(name string) (string, error) {
	path := strings.TrimSpace(os.Getenv(name))
	if path == "" {
		return "", nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}
	return strings.TrimSpace(string(contents)), nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
