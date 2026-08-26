package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/JDinSeattle/velaserve/internal/artifacts"
)

func main() {
	root := flag.String("artifact-root", "", "existing artifact bundle root")
	relative := flag.String("relative", "", "regular file path relative to the artifact root")
	flag.Parse()
	entry, err := artifacts.Record(*root, *relative)
	if err != nil {
		fmt.Fprintf(os.Stderr, "artifact-record: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("artifact-record: %s %s %d\n", entry.RelativePath, entry.SHA256, entry.Bytes)
}
