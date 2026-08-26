package main

import (
	"flag"
	"fmt"
	"os"

	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

func main() {
	input := flag.String("input", "", "binding-marked raw vLLM observer log")
	output := flag.String("output", "", "new normalized vLLM runtime JSONL")
	flag.Parse()
	count, err := placementrecorder.NormalizePinnedVLLMLog(*input, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Printf("vllm-normalize: wrote %d records to %s\n", count, *output)
}
