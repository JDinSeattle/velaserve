package placementrecorder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// NormalizePinnedEPPLog extracts only records emitted by the observational
// patch applied to the exact preregistered llm-d-router commit. Unrelated pod
// logs are ignored; a malformed observer record fails the whole conversion.
func NormalizePinnedEPPLog(inputPath, outputPath string) (uint64, error) {
	input, err := os.Open(inputPath)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	success := false
	defer func() {
		_ = output.Close()
		if !success {
			_ = os.Remove(outputPath)
		}
	}()
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxRecorderLineBytes)
	encoder := json.NewEncoder(output)
	var count uint64
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if !bytes.Contains(line, []byte("VELASERVE_EPP_RECORD ")) {
			continue
		}
		record, err := ParseEPP(line)
		if err != nil {
			return count, fmt.Errorf("observer record %d: %w", count+1, err)
		}
		if err := encoder.Encode(record); err != nil {
			return count, err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, err
	}
	if count == 0 {
		return 0, fmt.Errorf("pinned EPP log contains no VELASERVE_EPP_RECORD entries")
	}
	if err := output.Sync(); err != nil {
		return count, err
	}
	if err := output.Close(); err != nil {
		return count, err
	}
	success = true
	return count, nil
}
