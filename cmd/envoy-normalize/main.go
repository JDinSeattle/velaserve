package main

import (
	"flag"
	"fmt"
	"os"

	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

func main() {
	input := flag.String("input", "", "binding-marked raw inner-Envoy per-pod log stream")
	output := flag.String("output", "", "new normalized Envoy JSONL output")
	flag.Parse()
	count, err := placementrecorder.NormalizePinnedEnvoyLog(*input, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("normalized_records=%d\n", count)
}
