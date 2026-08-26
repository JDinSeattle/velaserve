package main

import (
	"flag"
	"fmt"
	"os"
	"time"

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
	case "decide":
		err = decide(os.Args[2:])
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

func decide(args []string) error {
	flags := flag.NewFlagSet("decide", flag.ContinueOnError)
	evidencePath := flags.String("evidence", "", "gate evidence JSONL")
	preregistrationPath := flags.String("preregistration", "", "frozen preregistration file")
	ledgerPath := flags.String("ledger", "", "artifact ledger JSONL")
	outputPath := flags.String("output", "", "decision JSON output")
	decisionID := flags.String("id", "", "opaque decision ID")
	complete := flags.Bool("complete", false, "assert that every preregistered evidence cell is present")
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
	return gate.DecideFile(gate.DecideFileOptions{
		EvidencePath:        *evidencePath,
		PreregistrationPath: *preregistrationPath,
		LedgerPath:          *ledgerPath,
		OutputPath:          *outputPath,
		DecisionID:          *decisionID,
		EvidenceComplete:    *complete,
		DecidedAt:           decidedAt,
	})
}

func sign(args []string) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	decisionPath := flags.String("decision", "", "unsigned decision JSON")
	privatePath := flags.String("private", "", "Ed25519 private key")
	outputPath := flags.String("output", "", "signed decision JSON output")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return gate.SignDecisionFile(*decisionPath, *privatePath, *outputPath)
}

func verify(args []string) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	signedPath := flags.String("signed", "", "signed decision JSON")
	publicPath := flags.String("public", "", "Ed25519 public key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	return gate.VerifyDecisionFile(*signedPath, *publicPath)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: velaserve-gate <keygen|decide|sign|verify> [flags]")
	os.Exit(2)
}
