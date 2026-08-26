package conditiondriver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerRejectsUnauthenticatedMutation(t *testing.T) {
	handler := Handler{ControlToken: "test-control-token"}
	request := httptest.NewRequest(http.MethodPost, "/v1/conditions/apply", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.Code)
	}
}
