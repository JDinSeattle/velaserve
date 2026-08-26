package conditiondriver

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/JDinSeattle/velaserve/internal/conditioncontroller"
)

type Handler struct {
	Driver       *Driver
	ControlToken string
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
		if handler.Driver == nil {
			http.Error(writer, "driver is not configured", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodPost || (request.URL.Path != "/v1/conditions/apply" && request.URL.Path != "/v1/conditions/finalize") {
		http.NotFound(writer, request)
		return
	}
	if strings.TrimSpace(handler.ControlToken) == "" {
		http.Error(writer, "driver is not configured", http.StatusServiceUnavailable)
		return
	}
	if subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+handler.ControlToken)) != 1 {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	if handler.Driver == nil {
		http.Error(writer, "driver is not configured", http.StatusServiceUnavailable)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 9<<20))
	decoder.DisallowUnknownFields()
	if request.URL.Path == "/v1/conditions/finalize" {
		var desired conditioncontroller.FinalizeRequest
		if err := decoder.Decode(&desired); err != nil {
			http.Error(writer, "invalid condition finalization request", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(writer, "condition finalization request must contain exactly one JSON object", http.StatusBadRequest)
			return
		}
		state, err := handler.Driver.Finalize(request.Context(), desired)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusConflict)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(state)
		return
	}
	var desired conditioncontroller.Request
	if err := decoder.Decode(&desired); err != nil {
		http.Error(writer, "invalid condition request", http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		http.Error(writer, "condition request must contain exactly one JSON object", http.StatusBadRequest)
		return
	}
	state, err := handler.Driver.Apply(request.Context(), desired)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(state)
}
