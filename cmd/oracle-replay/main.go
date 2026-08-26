package main

import (
	"flag"
	"fmt"
	"os"

	replay "github.com/JDinSeattle/velaserve/research/oracle-replay"
)

func main() {
	groups := flag.String("groups", "", "group-result JSONL input with workload coordinates")
	placements := flag.String("placements", "", "placement-event JSONL input")
	calibration := flag.String("calibration", "", "frozen replay calibration YAML")
	output := flag.String("output", "", "oracle-result JSONL output")
	appendOutput := flag.Bool("append", false, "append to an existing output instead of refusing it")
	flag.Parse()
	if err := replay.ReplayFile(replay.FileOptions{
		GroupsPath:      *groups,
		PlacementsPath:  *placements,
		CalibrationPath: *calibration,
		OutputPath:      *output,
		Append:          *appendOutput,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "oracle-replay: %v\n", err)
		os.Exit(1)
	}
}
