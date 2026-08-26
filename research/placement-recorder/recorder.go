package placementrecorder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JDinSeattle/velaserve/internal/evidence"
)

const (
	EPPSchedulingSchemaVersion = "velaserve.epp-scheduling/v1"
	maxRecorderLineBytes       = 16 << 20
)

type EPPRecord struct {
	SchemaVersion string                    `json:"schema_version"`
	RequestID     string                    `json:"request_id"`
	GroupID       string                    `json:"group_id"`
	ObservedAt    time.Time                 `json:"observed_at"`
	Snapshot      evidence.EndpointSnapshot `json:"snapshot"`
	Target        evidence.EndpointRef      `json:"target"`
}

type IngestOptions struct {
	ArtifactRoot string
	GroupsPath   string
	EnvoyPath    string
	EPPPath      string
}

type IngestReport struct {
	Complete       bool   `json:"complete"`
	PlacementCount uint64 `json:"placement_count"`
	UnmatchedCount uint64 `json:"unmatched_count"`
	PlacementsPath string `json:"placements_path"`
	UnmatchedPath  string `json:"unmatched_path"`
}

type UnmatchedRecord struct {
	Source string `json:"source"`
	Line   uint64 `json:"line"`
	Reason string `json:"reason"`
	Raw    string `json:"raw"`
}

type inputLine[T any] struct {
	value T
	line  uint64
	raw   string
}

func ParseEPP(line []byte) (EPPRecord, error) {
	const marker = "VELASERVE_EPP_RECORD "
	if index := bytes.Index(line, []byte(marker)); index >= 0 {
		line = line[index+len(marker):]
		if end := bytes.IndexByte(line, '\n'); end >= 0 {
			line = line[:end]
		}
	}
	var record EPPRecord
	if err := decodeStrict(line, &record); err != nil {
		return EPPRecord{}, fmt.Errorf("decode EPP scheduling record: %w", err)
	}
	if record.SchemaVersion != EPPSchedulingSchemaVersion {
		return EPPRecord{}, fmt.Errorf("schema_version: got %q, want %q", record.SchemaVersion, EPPSchedulingSchemaVersion)
	}
	if strings.TrimSpace(record.RequestID) == "" {
		return EPPRecord{}, fmt.Errorf("request_id: required")
	}
	if strings.TrimSpace(record.GroupID) == "" {
		return EPPRecord{}, fmt.Errorf("group_id: required")
	}
	if record.ObservedAt.IsZero() {
		return EPPRecord{}, fmt.Errorf("observed_at: required")
	}
	return record, nil
}

// Correlate joins request identity from the public edge, scheduling evidence
// from EPP, and the benchmark workload cell. None of the three sources is
// treated as a substitute for another.
func Correlate(envoy EnvoyRecord, epp EPPRecord, group evidence.GroupResult) (evidence.PlacementEvent, error) {
	if strings.TrimSpace(envoy.RequestID) == "" {
		return evidence.PlacementEvent{}, fmt.Errorf("request_id: missing from Envoy record")
	}
	if strings.TrimSpace(envoy.GroupID) == "" {
		return evidence.PlacementEvent{}, fmt.Errorf("group_id: missing from Envoy record")
	}
	if envoy.GroupID != group.GroupID || epp.GroupID != group.GroupID {
		return evidence.PlacementEvent{}, fmt.Errorf("group_id: Envoy, EPP, and benchmark identities do not agree")
	}
	if epp.RequestID != envoy.RequestID {
		return evidence.PlacementEvent{}, fmt.Errorf("request_id: Envoy and EPP identities do not agree")
	}
	childFound := false
	for _, child := range group.Children {
		if child.RequestID == envoy.RequestID {
			childFound = true
			break
		}
	}
	if !childFound {
		return evidence.PlacementEvent{}, fmt.Errorf("request_id: %q is absent from benchmark group", envoy.RequestID)
	}
	attributes := map[string]string{
		"envoy.upstream_host": envoy.UpstreamHost,
		"envoy.response_code": strconv.FormatUint(uint64(envoy.ResponseCode), 10),
		"envoy.duration_ms":   strconv.FormatUint(envoy.DurationMS, 10),
	}
	if envoy.TraceID != "" {
		attributes["trace_id"] = envoy.TraceID
	}
	if envoy.SpanID != "" {
		attributes["span_id"] = envoy.SpanID
	}
	event := evidence.PlacementEvent{
		SchemaVersion: evidence.SchemaVersion,
		RunID:         group.RunID,
		Arm:           group.Arm,
		GroupID:       group.GroupID,
		RequestID:     envoy.RequestID,
		FanoutWidth:   group.FanoutWidth,
		ArrivalSkewMS: group.Cell.ArrivalSkewMS,
		EPPReplicas:   group.Cell.EPPReplicas,
		LoadRegime:    group.Cell.LoadRegime,
		Snapshot:      epp.Snapshot,
		Target:        epp.Target,
		ObservedAt:    epp.ObservedAt.UTC(),
		Attributes:    attributes,
	}
	if err := evidence.ValidatePlacementEvent(event); err != nil {
		return evidence.PlacementEvent{}, fmt.Errorf("correlated placement is invalid: %w", err)
	}
	return event, nil
}

// Ingest writes placements and unmatched records once. Any unparseable,
// duplicate, or uncorrelated input makes Complete false without being dropped.
func Ingest(ctx context.Context, options IngestOptions) (IngestReport, error) {
	if ctx == nil {
		return IngestReport{}, fmt.Errorf("context is required")
	}
	root, err := secureRoot(options.ArtifactRoot)
	if err != nil {
		return IngestReport{}, err
	}
	groupsPath, err := secureInput(root, options.GroupsPath)
	if err != nil {
		return IngestReport{}, fmt.Errorf("groups input: %w", err)
	}
	envoyPath, err := secureInput(root, options.EnvoyPath)
	if err != nil {
		return IngestReport{}, fmt.Errorf("Envoy input: %w", err)
	}
	eppPath, err := secureInput(root, options.EPPPath)
	if err != nil {
		return IngestReport{}, fmt.Errorf("EPP input: %w", err)
	}
	placementsPath := filepath.Join(root, "placements.jsonl")
	unmatchedPath := filepath.Join(root, "unmatched.jsonl")
	placements, err := newRecordSink(placementsPath)
	if err != nil {
		return IngestReport{}, err
	}
	defer placements.close()
	unmatched, err := newRecordSink(unmatchedPath)
	if err != nil {
		return IngestReport{}, err
	}
	defer unmatched.close()
	report := IngestReport{PlacementsPath: placementsPath, UnmatchedPath: unmatchedPath}
	recordUnmatched := func(source string, line uint64, reason, raw string) error {
		report.UnmatchedCount++
		return unmatched.append(UnmatchedRecord{Source: source, Line: line, Reason: reason, Raw: raw})
	}

	groups := make(map[string]inputLine[evidence.GroupResult])
	childGroup := make(map[string]string)
	if err := scanLines(ctx, groupsPath, func(line uint64, raw []byte) error {
		var group evidence.GroupResult
		if err := decodeStrict(raw, &group); err != nil {
			return recordUnmatched("groups", line, err.Error(), string(raw))
		}
		if err := evidence.ValidateGroupResult(group); err != nil {
			return recordUnmatched("groups", line, err.Error(), string(raw))
		}
		if _, exists := groups[group.GroupID]; exists {
			return recordUnmatched("groups", line, "duplicate group_id", string(raw))
		}
		for _, child := range group.Children {
			if _, exists := childGroup[child.RequestID]; exists {
				return recordUnmatched("groups", line, "duplicate request_id", string(raw))
			}
			childGroup[child.RequestID] = group.GroupID
		}
		groups[group.GroupID] = inputLine[evidence.GroupResult]{value: group, line: line, raw: string(raw)}
		return nil
	}); err != nil {
		return IngestReport{}, err
	}

	envoyRecords := make(map[string]inputLine[EnvoyRecord])
	if err := scanLines(ctx, envoyPath, func(line uint64, raw []byte) error {
		record, err := ParseEnvoy(raw)
		if err != nil {
			return recordUnmatched("envoy", line, err.Error(), string(raw))
		}
		if _, exists := envoyRecords[record.RequestID]; exists {
			return recordUnmatched("envoy", line, "duplicate request_id", string(raw))
		}
		envoyRecords[record.RequestID] = inputLine[EnvoyRecord]{value: record, line: line, raw: string(raw)}
		return nil
	}); err != nil {
		return IngestReport{}, err
	}

	usedRequests := make(map[string]struct{})
	usedEnvoy := make(map[string]struct{})
	seenEPP := make(map[string]struct{})
	if err := scanLines(ctx, eppPath, func(line uint64, raw []byte) error {
		record, err := ParseEPP(raw)
		if err != nil {
			return recordUnmatched("epp", line, err.Error(), string(raw))
		}
		if _, exists := seenEPP[record.RequestID]; exists {
			return recordUnmatched("epp", line, "duplicate request_id", string(raw))
		}
		seenEPP[record.RequestID] = struct{}{}
		groupLine, exists := groups[record.GroupID]
		if !exists {
			return recordUnmatched("epp", line, "group_id has no benchmark record", string(raw))
		}
		envoyLine, exists := envoyRecords[record.RequestID]
		if !exists {
			return recordUnmatched("epp", line, "request_id has no Envoy record", string(raw))
		}
		event, err := Correlate(envoyLine.value, record, groupLine.value)
		if err != nil {
			return recordUnmatched("epp", line, err.Error(), string(raw))
		}
		if err := placements.append(event); err != nil {
			return err
		}
		report.PlacementCount++
		usedRequests[record.RequestID] = struct{}{}
		usedEnvoy[record.RequestID] = struct{}{}
		return nil
	}); err != nil {
		return IngestReport{}, err
	}

	for requestID, record := range envoyRecords {
		if _, used := usedEnvoy[requestID]; !used {
			if err := recordUnmatched("envoy", record.line, "request_id has no correlated EPP record", record.raw); err != nil {
				return IngestReport{}, err
			}
		}
	}
	for requestID, groupID := range childGroup {
		if _, used := usedRequests[requestID]; !used {
			group := groups[groupID]
			if err := recordUnmatched("groups", group.line, "benchmark child request_id has no correlated placement: "+requestID, group.raw); err != nil {
				return IngestReport{}, err
			}
		}
	}
	if err := placements.sync(); err != nil {
		return IngestReport{}, err
	}
	if err := unmatched.sync(); err != nil {
		return IngestReport{}, err
	}
	report.Complete = report.UnmatchedCount == 0
	return report, nil
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return fmt.Errorf("trailing JSON: %w", err)
	}
	return nil
}

func scanLines(ctx context.Context, path string, visit func(uint64, []byte) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxRecorderLineBytes)
	line := uint64(0)
	for scanner.Scan() {
		line++
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			if err := visit(line, raw); err != nil {
				return err
			}
			continue
		}
		if err := visit(line, bytes.Clone(raw)); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan %s after line %d: %w", path, line, err)
	}
	return nil
}

func secureRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("artifact root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve artifact root: %w", err)
	}
	return resolved, nil
}

func secureInput(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be non-empty and relative to artifact root")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes artifact root")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path escapes artifact root")
	}
	return resolved, nil
}

type recordSink struct {
	path    string
	file    *os.File
	encoder *json.Encoder
}

func newRecordSink(path string) (*recordSink, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	return &recordSink{path: path, file: file, encoder: json.NewEncoder(file)}, nil
}

func (sink *recordSink) append(value any) error {
	if err := sink.encoder.Encode(value); err != nil {
		return fmt.Errorf("append %s: %w", sink.path, err)
	}
	return nil
}

func (sink *recordSink) sync() error {
	if err := sink.file.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", sink.path, err)
	}
	return nil
}

func (sink *recordSink) close() {
	_ = sink.file.Close()
}
