package jsonl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureRecord struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
}

func TestAppendPreservesExistingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "events.jsonl")
	first := fixtureRecord{ID: "first", Value: 1}
	second := fixtureRecord{ID: "second", Value: 2}

	if err := Append(path, first); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if err := Append(path, second); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}

	got, err := Read[fixtureRecord](path)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := []fixtureRecord{first, second}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
}

func TestReadReportsMalformedLineNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	content := "{\"id\":\"first\",\"value\":1}\nnot-json\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Read[fixtureRecord](path)
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("Read() error = %v, want line 2", err)
	}
}

func TestReadRejectsTrailingJSONValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte("{\"id\":\"first\",\"value\":1} {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Read[fixtureRecord](path)
	if err == nil || !strings.Contains(err.Error(), "trailing JSON value") {
		t.Fatalf("Read() error = %v, want trailing JSON value", err)
	}
}
