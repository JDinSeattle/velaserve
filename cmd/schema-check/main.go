package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/JDinSeattle/velaserve/internal/schemacheck"
)

func main() {
	schemaDir := flag.String("schema-dir", "benchmarks/expected-schema", "directory containing VelaServe JSON schemas")
	fixtureDir := flag.String("fixture-dir", "internal/evidence/testdata", "directory containing valid and invalid fixtures")
	flag.Parse()

	report, err := schemacheck.ValidateDirectories(*schemaDir, *fixtureDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "schema-check: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("schema-check: %d valid fixtures accepted; %d invalid fixture rejected\n", report.ValidAccepted, report.InvalidRejected)
}
