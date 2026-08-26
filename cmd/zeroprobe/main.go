package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
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

func run(ctx context.Context, args []string) (any, error) {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	phase := flags.String("phase", "z0-a", "zeroing phase: z0-a, z0-b, or z0-c")
	profile := flags.String("profile", "research/preregistration/z0-v1.yaml", "frozen preregistration YAML")
	benchmarkProfile := flags.String("benchmark-profile", "benchmarks/profiles/local-sim.yaml", "fanoutbench workload YAML")
	artifactRoot := flags.String("artifact-root", "", "new artifact bundle directory")
	endpoint := flags.String("endpoint", os.Getenv("VELASERVE_ENDPOINT"), "OpenAI-compatible endpoint; or set VELASERVE_ENDPOINT")
	limit := flags.Uint64("limit", 0, "partial smoke limit; zero runs the complete matrix")
	maxEventBytes := flags.Int("max-event-bytes", 8<<20, "maximum bytes in one SSE event")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return zeroprobe.Run(ctx, zeroprobe.RunOptions{
		PrepareOptions: zeroprobe.PrepareOptions{
			Phase:               zeroprobe.Phase(*phase),
			PreregistrationPath: *profile,
			ArtifactRoot:        *artifactRoot,
		},
		BenchmarkProfilePath: *benchmarkProfile,
		Endpoint:             *endpoint,
		Limit:                *limit,
		MaxEventBytes:        *maxEventBytes,
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
	if err := flags.Parse(args); err != nil {
		return err
	}
	return zeroprobe.Analyze(zeroprobe.AnalyzeOptions{ArtifactRoot: *artifactRoot, CalibrationPath: *calibration})
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	artifactRoot := flags.String("artifact-root", "", "artifact bundle directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return zeroprobe.Verify(*artifactRoot)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: zeroprobe <run|ingest|analyze|verify> [flags]")
	os.Exit(2)
}
