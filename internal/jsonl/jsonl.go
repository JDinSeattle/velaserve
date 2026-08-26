package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxLineBytes = 16 << 20

func Append[T any](path string, value T) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal JSONL record: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create JSONL directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open JSONL file for append: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("append JSONL record: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync JSONL file: %w", err)
	}
	return nil
}

func Read[T any](path string) ([]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open JSONL file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	result := make([]T, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return nil, fmt.Errorf("decode JSONL line %d: empty line", lineNumber)
		}
		var value T
		decoder := json.NewDecoder(bytes.NewReader(line))
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode JSONL line %d: %w", lineNumber, err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			if err == nil {
				return nil, fmt.Errorf("decode JSONL line %d: trailing JSON value", lineNumber)
			}
			return nil, fmt.Errorf("decode JSONL line %d trailing JSON value: %w", lineNumber, err)
		}
		result = append(result, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan JSONL file after line %d: %w", lineNumber, err)
	}
	return result, nil
}
