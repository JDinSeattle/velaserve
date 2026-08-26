package placementrecorder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const StreamEmitterBindingSchemaVersion = "velaserve.stream-emitter-binding/v1"

type streamEmitterBinding struct {
	SchemaVersion string `json:"schema_version"`
	PodName       string `json:"pod_name"`
	PodUID        string `json:"pod_uid"`
}

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
	var emitter *streamEmitterBinding
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if bytes.Contains(line, []byte("VELASERVE_STREAM_BINDING ")) {
			binding, err := parseStreamEmitterBinding(line)
			if err != nil {
				return count, err
			}
			emitter = &binding
			continue
		}
		if !bytes.Contains(line, []byte("VELASERVE_EPP_RECORD ")) {
			continue
		}
		if emitter == nil {
			return count, fmt.Errorf("observer record %d has no preceding exact stream emitter binding", count+1)
		}
		record, err := ParseEPP(line)
		if err != nil {
			return count, fmt.Errorf("observer record %d: %w", count+1, err)
		}
		if record.EmitterPodName != "" || record.EmitterPodUID != "" {
			return count, fmt.Errorf("observer record %d attempted to self-assert emitter identity", count+1)
		}
		record.EmitterPodName = emitter.PodName
		record.EmitterPodUID = emitter.PodUID
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

// NormalizePinnedEnvoyLog converts binding-marked per-pod inner-Envoy streams
// into stable JSONL while attaching the collector-asserted pod name and UID.
func NormalizePinnedEnvoyLog(inputPath, outputPath string) (uint64, error) {
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
	var emitter *streamEmitterBinding
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if bytes.Contains(line, []byte("VELASERVE_STREAM_BINDING ")) {
			binding, err := parseStreamEmitterBinding(line)
			if err != nil {
				return count, err
			}
			emitter = &binding
			continue
		}
		objectStart := bytes.IndexByte(line, '{')
		if objectStart < 0 {
			continue
		}
		candidate := line[objectStart:]
		var raw map[string]any
		if err := json.Unmarshal(candidate, &raw); err != nil {
			if bytes.Contains(candidate, []byte("REQ(X-REQUEST-ID)")) || bytes.Contains(candidate, []byte("REQ(X-VELA-FANOUT-GROUP)")) {
				return count, fmt.Errorf("inner-Envoy candidate record %d is malformed: %w", count+1, err)
			}
			continue
		}
		requestID := stringValue(raw["REQ(X-REQUEST-ID)"])
		groupID := stringValue(raw["REQ(X-VELA-FANOUT-GROUP)"])
		if requestID == "" || requestID == "-" || groupID == "" || groupID == "-" {
			continue
		}
		if emitter == nil {
			return count, fmt.Errorf("inner-Envoy record %d has no preceding exact stream emitter binding", count+1)
		}
		if stringValue(raw["EMITTER_POD_NAME"]) != "" || stringValue(raw["EMITTER_POD_UID"]) != "" {
			return count, fmt.Errorf("inner-Envoy record %d attempted to self-assert emitter identity", count+1)
		}
		record, err := ParseEnvoy(candidate)
		if err != nil {
			return count, fmt.Errorf("inner-Envoy record %d: %w", count+1, err)
		}
		record.EmitterPodName = emitter.PodName
		record.EmitterPodUID = emitter.PodUID
		if err := encoder.Encode(record); err != nil {
			return count, err
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, err
	}
	if count == 0 {
		return 0, fmt.Errorf("pinned inner-Envoy log contains no VelaServe request records")
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

// NormalizePinnedVLLMLog extracts observer records from exact per-pod runtime
// streams and attaches the collector-controlled pod identity. The upstream
// observer is not allowed to assert its own emitter tuple.
func NormalizePinnedVLLMLog(inputPath, outputPath string) (uint64, error) {
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
	var emitter *streamEmitterBinding
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if bytes.Contains(line, []byte("VELASERVE_STREAM_BINDING ")) {
			binding, err := parseStreamEmitterBinding(line)
			if err != nil {
				return count, err
			}
			emitter = &binding
			continue
		}
		const marker = "VELASERVE_VLLM_RUNTIME "
		index := bytes.Index(line, []byte(marker))
		if index < 0 {
			continue
		}
		if emitter == nil {
			return count, fmt.Errorf("vLLM observer record %d has no preceding exact stream emitter binding", count+1)
		}
		var record VLLMRuntimeRecord
		if err := decodeStrict(line[index+len(marker):], &record); err != nil {
			return count, fmt.Errorf("vLLM observer record %d: %w", count+1, err)
		}
		if record.EmitterPodName != "" || record.EmitterPodUID != "" {
			return count, fmt.Errorf("vLLM observer record %d attempted to self-assert emitter identity", count+1)
		}
		record.EmitterPodName = emitter.PodName
		record.EmitterPodUID = emitter.PodUID
		if err := validateVLLMRuntime(record); err != nil {
			return count, fmt.Errorf("vLLM observer record %d: %w", count+1, err)
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
		return 0, fmt.Errorf("pinned vLLM log contains no VELASERVE_VLLM_RUNTIME entries")
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

func parseStreamEmitterBinding(line []byte) (streamEmitterBinding, error) {
	const marker = "VELASERVE_STREAM_BINDING "
	index := bytes.Index(line, []byte(marker))
	if index < 0 {
		return streamEmitterBinding{}, fmt.Errorf("stream emitter marker is missing")
	}
	decoder := json.NewDecoder(bytes.NewReader(line[index+len(marker):]))
	decoder.DisallowUnknownFields()
	var binding streamEmitterBinding
	if err := decoder.Decode(&binding); err != nil {
		return streamEmitterBinding{}, fmt.Errorf("decode stream emitter binding: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return streamEmitterBinding{}, fmt.Errorf("decode stream emitter binding: trailing JSON value")
		}
		return streamEmitterBinding{}, fmt.Errorf("decode stream emitter binding: %w", err)
	}
	if binding.SchemaVersion != StreamEmitterBindingSchemaVersion || strings.TrimSpace(binding.PodName) == "" || strings.TrimSpace(binding.PodUID) == "" {
		return streamEmitterBinding{}, fmt.Errorf("stream emitter binding is incomplete")
	}
	return binding, nil
}
