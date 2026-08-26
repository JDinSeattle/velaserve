package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	gatecompiler "github.com/JDinSeattle/velaserve/research/gate-compiler"
	gate "github.com/JDinSeattle/velaserve/research/gate-decision"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "compile":
		err = compile(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "velaserve-gate %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}

func keygen(args []string) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	privatePath := flags.String("private", "", "private key output")
	publicPath := flags.String("public", "", "public key output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return gate.GenerateKeyFiles(*privatePath, *publicPath)
}

func compile(args []string) error {
	flags := flag.NewFlagSet("compile", flag.ContinueOnError)
	bundlePaths := flags.String("bundles", "", "comma-separated verified Z0-B/Z0-C bundle directories")
	preregistrationPath := flags.String("preregistration", "", "frozen preregistration file")
	outputDirectory := flags.String("output-dir", "", "new compiled gate directory")
	decisionID := flags.String("id", "", "opaque decision ID")
	decidedAtText := flags.String("decided-at", "", "RFC3339 decision timestamp; defaults to current UTC time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	decidedAt := time.Now().UTC()
	if *decidedAtText != "" {
		parsed, err := time.Parse(time.RFC3339, *decidedAtText)
		if err != nil {
			return fmt.Errorf("parse decided-at: %w", err)
		}
		decidedAt = parsed
	}
	var bundles []string
	for _, path := range strings.Split(*bundlePaths, ",") {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			bundles = append(bundles, trimmed)
		}
	}
	_, err := gatecompiler.Compile(gatecompiler.Options{
		BundleRoots:         bundles,
		PreregistrationPath: *preregistrationPath,
		OutputDirectory:     *outputDirectory,
		DecisionID:          *decisionID,
		DecidedAt:           decidedAt,
	})
	return err
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	decisionPath := flags.String("decision", "", "unsigned decision JSON")
	privatePath := flags.String("private", "", "Ed25519 private key")
	outputPath := flags.String("output", "", "signed decision JSON output")
	evidencePath := flags.String("evidence", "", "bound gate evidence JSONL")
	preregistrationPath := flags.String("preregistration", "", "bound frozen preregistration")
	ledgerPath := flags.String("ledger", "", "bound aggregate artifact ledger")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return gate.SignDecisionFile(*decisionPath, *privatePath, *outputPath, gate.DecisionBindings{EvidencePath: *evidencePath, PreregistrationPath: *preregistrationPath, LedgerPath: *ledgerPath})
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	signedPath := flags.String("signed", "", "signed decision JSON")
	publicPath := flags.String("public", "", "Ed25519 public key")
	evidencePath := flags.String("evidence", "", "bound gate evidence JSONL")
	preregistrationPath := flags.String("preregistration", "", "bound frozen preregistration")
	ledgerPath := flags.String("ledger", "", "bound aggregate artifact ledger")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return gate.VerifyDecisionFile(*signedPath, *publicPath, gate.DecisionBindings{EvidencePath: *evidencePath, PreregistrationPath: *preregistrationPath, LedgerPath: *ledgerPath})
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: velaserve-gate <keygen|compile|sign|verify> [flags]")
	os.Exit(2)
}
