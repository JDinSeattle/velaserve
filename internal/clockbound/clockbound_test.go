package clockbound

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckSelectsBoundedBestSample(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(Handler())
	t.Cleanup(server.Close)
	observation, err := Check(context.Background(), server.Client(), server.URL+"/v1/time", 3, 250*time.Millisecond, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if observation.SchemaVersion != ObservationSchemaVersion || observation.Samples != 3 || observation.BestRTTNanoseconds <= 0 || observation.OffsetMinimumNanoseconds > observation.OffsetMaximumNanoseconds || observation.ObservedAt.IsZero() {
		t.Fatalf("invalid observation: %+v", observation)
	}
}

func TestCheckRejectsClockOutsideBound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writeTimeResponse(writer, time.Now().UTC().Add(time.Second))
	}))
	t.Cleanup(server.Close)
	if _, err := Check(context.Background(), server.Client(), server.URL, 2, 250*time.Millisecond, 10*time.Millisecond); err == nil {
		t.Fatal("out-of-bound clock unexpectedly passed")
	}
}
