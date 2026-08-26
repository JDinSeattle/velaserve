package main

import (
	"path/filepath"
	"testing"

	zeroprobe "github.com/JDinSeattle/velaserve/research/zeroprobe"
)

func TestHashPreflightReturnsValidatedInvariant(t *testing.T) {
	path := filepath.Join("..", "..", "internal", "evidence", "testdata", "preflight-valid.json")
	binding, err := zeroprobe.LoadPreflightBinding(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := binding.InvariantSHA256()
	if err != nil {
		t.Fatal(err)
	}
	result, err := hashPreflight([]string{"--path", path})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := result.(map[string]string)
	if !ok || got["invariant_sha256"] != want {
		t.Fatalf("hashPreflight() = %#v, want invariant_sha256 %q", result, want)
	}
}

func TestHashPreflightRequiresOnlyPathFlag(t *testing.T) {
	if _, err := hashPreflight(nil); err == nil {
		t.Fatal("hashPreflight() accepted a missing path")
	}
	if _, err := hashPreflight([]string{"--path", "binding.json", "extra"}); err == nil {
		t.Fatal("hashPreflight() accepted a positional argument")
	}
}
