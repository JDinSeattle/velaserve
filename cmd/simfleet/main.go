package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JDinSeattle/velaserve/internal/simfleet"
)

func main() {
	address := flag.String("listen", "127.0.0.1:8000", "HTTP listen address")
	seed := flag.Uint64("seed", 20260825, "deterministic simulator seed")
	logDirectory := flag.String("log-dir", "", "write epp.jsonl and envoy.jsonl here on shutdown")
	flag.Parse()

	config := simfleet.DefaultConfig()
	config.Seed = *seed
	fleet, err := simfleet.New(config)
	if err != nil {
		fatal(err)
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		fatal(err)
	}
	server := &http.Server{Handler: fleet, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	fmt.Printf("simfleet: simulation-only endpoint http://%s/v1/chat/completions\n", listener.Addr())

	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-signalContext.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			fatal(err)
		}
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		fatal(err)
	}
	if *logDirectory != "" {
		if err := fleet.WriteLogs(*logDirectory); err != nil {
			fatal(err)
		}
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "simfleet: %v\n", err)
	os.Exit(1)
}
