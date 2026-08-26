package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/JDinSeattle/velaserve/internal/bench"
	"github.com/JDinSeattle/velaserve/internal/jsonl"
)

func main() {
	profilePath := flag.String("profile", "", "frozen benchmark profile YAML")
	endpoint := flag.String("endpoint", "", "OpenAI-compatible /v1/chat/completions URL")
	outputPath := flag.String("output", "", "append-only group-result JSONL output")
	appendOutput := flag.Bool("append", false, "append to an existing output file")
	limit := flag.Uint64("limit", 0, "run at most this many expanded groups; zero runs all")
	maxEventBytes := flag.Int("max-event-bytes", 8<<20, "maximum bytes in one SSE event")
	dryRun := flag.Bool("dry-run", false, "validate and report expanded group count without sending traffic")
	flag.Parse()

	if err := run(options{
		profilePath:   *profilePath,
		endpoint:      *endpoint,
		outputPath:    *outputPath,
		appendOutput:  *appendOutput,
		limit:         *limit,
		maxEventBytes: *maxEventBytes,
		dryRun:        *dryRun,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "fanoutbench: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	profilePath   string
	endpoint      string
	outputPath    string
	appendOutput  bool
	limit         uint64
	maxEventBytes int
	dryRun        bool
}

func run(options options) error {
	profile, err := bench.LoadProfile(options.profilePath)
	if err != nil {
		return err
	}
	requests, err := bench.ExpandProfile(profile)
	if err != nil {
		return err
	}
	if options.limit > 0 && options.limit < uint64(len(requests)) {
		requests = requests[:options.limit]
	}
	if options.dryRun {
		fmt.Printf("fanoutbench: profile expands to %d selected groups\n", len(requests))
		return nil
	}
	if options.endpoint == "" {
		return fmt.Errorf("endpoint is required unless --dry-run is used")
	}
	if options.outputPath == "" {
		return fmt.Errorf("output is required unless --dry-run is used")
	}
	if info, err := os.Lstat(options.outputPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output path is a symbolic link")
		}
		if !options.appendOutput {
			return fmt.Errorf("output already exists; pass --append to retain and extend it")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := bench.Client{Endpoint: options.endpoint, MaxEventBytes: options.maxEventBytes}
	for index, request := range requests {
		result, err := client.RunGroup(ctx, request)
		if err != nil {
			return fmt.Errorf("group %d (%s): %w", index+1, request.RunID, err)
		}
		if err := jsonl.Append(options.outputPath, result); err != nil {
			return fmt.Errorf("persist group %d (%s): %w", index+1, request.RunID, err)
		}
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted after persisting group %d: %w", index+1, ctx.Err())
		}
	}
	fmt.Printf("fanoutbench: persisted %d groups to %s\n", len(requests), options.outputPath)
	return nil
}
