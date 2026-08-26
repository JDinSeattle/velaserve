package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JDinSeattle/velaserve/internal/clockbound"
)

func main() {
	if len(os.Args) < 2 {
		fatal(fmt.Errorf("usage: clock-probe serve|check [flags]"))
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "check":
		err = check(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", ":8084", "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *listen == "" {
		return fmt.Errorf("listen address is required and positional arguments are not accepted")
	}
	server := &http.Server{Addr: *listen, Handler: clockbound.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func check(args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	endpoint := flags.String("url", "", "exact node-local clock probe URL")
	samples := flags.Uint("samples", 5, "number of RTT-bounded samples")
	maximumRTT := flags.Duration("max-rtt", 250*time.Millisecond, "maximum accepted round-trip time")
	maximumOffset := flags.Duration("max-offset", 100*time.Millisecond, "maximum accepted absolute clock offset")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *endpoint == "" {
		return fmt.Errorf("clock URL is required and positional arguments are not accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*samples+1)**maximumRTT)
	defer cancel()
	observation, err := clockbound.Check(ctx, &http.Client{}, *endpoint, uint32(*samples), *maximumRTT, *maximumOffset)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "clock-probe:", err)
	os.Exit(2)
}
