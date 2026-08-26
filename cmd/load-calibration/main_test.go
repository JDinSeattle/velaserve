package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseRatesRequiresStrictFiniteRamp(t *testing.T) {
	t.Parallel()
	want := []float64{.5, 1, 2, 4, 8}
	got, err := parseRates("0.5,1,2,4,8")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("parse rates = %v, %v", got, err)
	}
	for _, value := range []string{"1,1,2,3,4", "1,NaN,2,3,4", "1,+Inf,2,3,4", "1,0,2,3,4"} {
		if _, err := parseRates(value); err == nil {
			t.Fatalf("parseRates(%q) unexpectedly succeeded", value)
		}
	}
}

func TestWriteExclusiveAtomicPublishesCompleteFileWithoutOverwrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := writeExclusiveAtomic(path, []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeExclusiveAtomic(path, []byte("second\n")); err == nil {
		t.Fatal("overwrite unexpectedly succeeded")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "first\n" {
		t.Fatalf("published contents changed: %q", contents)
	}
}

func TestReadCalibrationEndpointsRequiresExactPodUIDAndMetricsShape(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "endpoints.json")
	contents := `[{"id":"model-0","pod_uid":"uid-0","metrics_url":"http://model-0.example:8200/metrics"}]`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	endpoints, err := readCalibrationEndpoints(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].ID != "model-0" || endpoints[0].PodUID != "uid-0" {
		t.Fatalf("endpoints = %#v", endpoints)
	}
	if err := os.WriteFile(path, []byte(`[{"id":"model-0","pod_uid":"uid-0","metrics_url":"http://model/metrics","unexpected":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCalibrationEndpoints(path); err == nil {
		t.Fatal("unknown endpoint field was accepted")
	}
}
