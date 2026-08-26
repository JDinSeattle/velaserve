package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result any
	var err error
	switch os.Args[1] {
	case "run":
		result, err = run(ctx, os.Args[2:])
	case "ingest":
		result, err = ingest(ctx, os.Args[2:])
	case "analyze":
		err = analyze(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "seal":
		err = seal(os.Args[2:])
	case "validate-preflight":
		err = validatePreflight(os.Args[2:])
	case "hash-preflight":
		result, err = hashPreflight(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "zeroprobe %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	if result != nil {
		encoded, _ := json.Marshal(result)
		fmt.Println(string(encoded))
	}
}

func hashPreflight(args []string) (any, error) {
	flags := flag.NewFlagSet("hash-preflight", flag.ContinueOnError)
	path := flags.String("path", "", "preflight binding JSON")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*path) == "" {
		return nil, fmt.Errorf("--path is required and positional arguments are not accepted")
	}
	binding, err := zeroprobe.LoadPreflightBinding(*path)
	if err != nil {
		return nil, err
	}
	digest, err := binding.InvariantSHA256()
	if err != nil {
		return nil, err
	}
	return map[string]string{"invariant_sha256": digest}, nil
}

func validatePreflight(args []string) error {
	flags := flag.NewFlagSet("validate-preflight", flag.ContinueOnError)
	path := flags.String("path", "", "preflight binding JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*path) == "" {
		return fmt.Errorf("--path is required")
	}
	_, err := zeroprobe.LoadPreflightBinding(*path)
	return err
}

func run(ctx context.Context, args []string) (any, error) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	phase := flags.String("phase", "z0-a", "zeroing phase: z0-a, z0-b, or z0-c")
	profile := flags.String("profile", "research/preregistration/z0-v1.yaml", "frozen preregistration YAML")
	benchmarkProfile := flags.String("benchmark-profile", "benchmarks/profiles/local-sim.yaml", "fanoutbench workload YAML")
	artifactRoot := flags.String("artifact-root", "", "new artifact bundle directory")
	endpoint := flags.String("endpoint", os.Getenv("VELASERVE_ENDPOINT"), "OpenAI-compatible endpoint; or set VELASERVE_ENDPOINT")
	limit := flags.Uint64("limit", 0, "partial smoke limit; zero runs the complete matrix")
	maxEventBytes := flags.Int("max-event-bytes", 8<<20, "maximum bytes in one SSE event")
	simulator := flags.Bool("simulator", false, "enable X-Sim metadata and target correlation; never use for cloud runs")
	conditionController := flags.String("condition-controller", "", "HTTP endpoint that applies and attests every real workload condition")
	conditionControlToken := strings.TrimSpace(os.Getenv("VELASERVE_CONDITION_CONTROL_TOKEN"))
	requireCondition := flags.Bool("require-condition-attestation", false, "fail before inference unless every workload condition is attested")
	preflightBinding := flags.String("preflight-binding", "", "immutable deployment binding emitted by cloud-preflight")
	profileCalibration := flags.String("profile-calibration", "", "raw tokenizer and crossover calibration bound by cloud-preflight")
	loadCalibration := flags.String("load-calibration", "", "raw saturation sweep bound by cloud-preflight")
	crossoverBundle := flags.String("crossover-bundle", "", "sealed raw crossover calibration bundle")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return zeroprobe.Run(ctx, zeroprobe.RunOptions{
		PrepareOptions: zeroprobe.PrepareOptions{
			Phase:               zeroprobe.Phase(*phase),
			PreregistrationPath: *profile,
			ArtifactRoot:        *artifactRoot,
		},
		BenchmarkProfilePath:        *benchmarkProfile,
		Endpoint:                    *endpoint,
		Limit:                       *limit,
		MaxEventBytes:               *maxEventBytes,
		SimulatorMode:               *simulator,
		ConditionControllerEndpoint: *conditionController,
		ConditionControlToken:       conditionControlToken,
		RequireConditionAttestation: *requireCondition,
		PreflightBindingPath:        *preflightBinding,
		ProfileCalibrationPath:      *profileCalibration,
		LoadCalibrationPath:         *loadCalibration,
		CrossoverBundlePath:         *crossoverBundle,
	})
}

func ingest(ctx context.Context, args []string) (any, error) {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	artifactRoot := flags.String("artifact-root", "", "artifact bundle directory")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return zeroprobe.Ingest(ctx, *artifactRoot)
}

func analyze(args []string) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	artifactRoot := flags.String("artifact-root", "", "artifact bundle directory")
	calibration := flags.String("calibration", "benchmarks/profiles/oracle-local-sim.yaml", "frozen oracle calibration YAML")
	evidenceScope := flags.String("evidence-scope", "simulation_only", "simulation_only or real_gpu")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return zeroprobe.Analyze(zeroprobe.AnalyzeOptions{ArtifactRoot: *artifactRoot, CalibrationPath: *calibration, EvidenceScope: *evidenceScope})
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	artifactRoot := flags.String("artifact-root", "", "artifact bundle directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return zeroprobe.Verify(*artifactRoot)
}

func seal(args []string) error {
	flags := flag.NewFlagSet("seal", flag.ContinueOnError)
	artifactRoot := flags.String("artifact-root", "", "artifact bundle directory to verify and seal exactly once")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return zeroprobe.SealVerification(*artifactRoot)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: zeroprobe <run|ingest|analyze|seal|verify|validate-preflight|hash-preflight> [flags]")
	os.Exit(2)
}
