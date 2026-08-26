package main

import (
	"flag"
	"fmt"
	"os"

	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
)

func main() {
	groups := flag.String("groups", "", "condition-attested group results JSONL")
	transfers := flag.String("transfers", "", "measured model-runtime acquisition telemetry JSONL")
	output := flag.String("output", "", "new normalized source-pressure JSONL")
	flag.Parse()
	count, err := placementrecorder.CompileSourcePressure(*groups, *transfers, *output)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("compiled_records=%d\n", count)
}
