package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/conditioncontroller"
)

func main() {
	listen := strings.TrimSpace(os.Getenv("VELASERVE_CONDITION_LISTEN"))
	if listen == "" {
		listen = ":8082"
	}
	driverURL := strings.TrimSpace(os.Getenv("VELASERVE_CONDITION_DRIVER_URL"))
	revision := strings.TrimSpace(os.Getenv("VELASERVE_CONTROLLER_REVISION"))
	controlToken := strings.TrimSpace(os.Getenv("VELASERVE_CONDITION_CONTROL_TOKEN"))
	applyTimeoutSeconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv("VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS")))
	if driverURL == "" || revision == "" || controlToken == "" || err != nil || applyTimeoutSeconds < 600 || applyTimeoutSeconds > 3600 {
		fmt.Fprintln(os.Stderr, "VELASERVE_CONDITION_DRIVER_URL, VELASERVE_CONTROLLER_REVISION, VELASERVE_CONDITION_CONTROL_TOKEN, and a 600..3600 second VELASERVE_CONDITION_APPLY_TIMEOUT_SECONDS are required")
		os.Exit(2)
	}
	controller := conditioncontroller.Controller{Driver: conditioncontroller.HTTPDriver{Endpoint: driverURL, Token: controlToken, Timeout: time.Duration(applyTimeoutSeconds) * time.Second}, Revision: revision, ControlToken: controlToken}
	server := &http.Server{Addr: listen, Handler: controller, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
