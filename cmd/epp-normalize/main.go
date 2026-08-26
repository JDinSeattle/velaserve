package main

import (
	"flag"
	"fmt"
	"os"

	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

func main() {
	input := flag.String("input", "", "raw log from the pinned observational EPP build")
	output := flag.String("output", "", "new normalized EPP JSONL output")
	flag.Parse()
	count, err := placementrecorder.NormalizePinnedEPPLog(*input, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("normalized_records=%d\n", count)
}
