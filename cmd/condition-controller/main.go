package main

import (
	"fmt"
	"net/http"
	"os"
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
	if driverURL == "" || revision == "" {
		fmt.Fprintln(os.Stderr, "VELASERVE_CONDITION_DRIVER_URL and VELASERVE_CONTROLLER_REVISION are required")
		os.Exit(2)
	}
	controller := conditioncontroller.Controller{Driver: conditioncontroller.HTTPDriver{Endpoint: driverURL}, Revision: revision}
	server := &http.Server{Addr: listen, Handler: controller, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
