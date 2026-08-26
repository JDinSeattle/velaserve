package main

import (
	"flag"
	"fmt"
	"os"

	placementrecorder "github.com/JDinSeattle/velaserve/research/placement-recorder"
	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

func main() {
	groups := flag.String("groups", "", "condition-attested group results JSONL")
	placements := flag.String("placements", "", "correlated EPP placement events JSONL")
	runtime := flag.String("runtime", "", "raw records from the pinned vLLM observer JSONL")
	preflightBinding := flag.String("preflight-binding", "", "validated deployment binding containing exact model pod identities")
	output := flag.String("output", "", "new normalized acquisition telemetry JSONL")
	flag.Parse()
	binding, err := zeroprobe.LoadPreflightBinding(*preflightBinding)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	emitters := make([]placementrecorder.RuntimeEmitterBinding, 0, len(binding.Model.Pods))
	for _, pod := range binding.Model.Pods {
		emitters = append(emitters, placementrecorder.RuntimeEmitterBinding{PodName: pod.Name, PodUID: pod.UID})
	}
	count, err := placementrecorder.NormalizeVLLMRuntime(*groups, *placements, *runtime, *output, emitters)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("normalized_records=%d\n", count)
}
