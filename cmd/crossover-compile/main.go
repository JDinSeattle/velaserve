package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/JDinSeattle/velaserve/research/crossover"
)

func main() {
	verifyBundle := flag.String("verify-bundle", "", "verify an existing sealed crossover bundle")
	bindingPath := flag.String("binding", "", "exact calibration deployment binding JSON")
	observationsPath := flag.String("observations", "", "raw forced recompute/P2P observations JSONL")
	runtimeRawPath := flag.String("vllm-raw", "", "binding-marked raw pinned-vLLM log")
	outputDirectory := flag.String("output-dir", "", "new sealed crossover bundle directory")
	flag.Parse()
	if *verifyBundle != "" {
		evidence, err := crossover.VerifyBundle(*verifyBundle)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Printf("crossover-compile: verified %d raw-derived trials with minCachedTokenDelta=%d\n", len(evidence.Trials), evidence.MinCachedTokenDelta)
		return
	}
	evidence, err := crossover.Compile(crossover.CompileOptions{
		BindingPath: *bindingPath, ObservationsPath: *observationsPath,
		RuntimeRawPath: *runtimeRawPath, OutputDirectory: *outputDirectory,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("crossover-compile: sealed %d trials with measured minCachedTokenDelta=%d in %s\n", len(evidence.Trials), evidence.MinCachedTokenDelta, *outputDirectory)
}
