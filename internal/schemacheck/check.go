package schemacheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Report struct {
	ValidAccepted   int
	InvalidRejected int
}

func ValidateDirectories(schemaDir, fixtureDir string) (Report, error) {
	schemas, err := compileSchemas(schemaDir)
	if err != nil {
		return Report{}, err
	}
	entries, err := os.ReadDir(fixtureDir)
	if err != nil {
		return Report{}, fmt.Errorf("read fixture directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	report := Report{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		kind, expectedValid, err := classifyFixture(entry.Name())
		if err != nil {
			return Report{}, err
		}
		schema, ok := schemas[kind]
		if !ok {
			return Report{}, fmt.Errorf("fixture %q has no compiled schema for kind %q", entry.Name(), kind)
		}
		path := filepath.Join(fixtureDir, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			return Report{}, fmt.Errorf("open fixture %q: %w", entry.Name(), err)
		}
		instance, decodeErr := jsonschema.UnmarshalJSON(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return Report{}, fmt.Errorf("decode fixture %q: %w", entry.Name(), decodeErr)
		}
		if closeErr != nil {
			return Report{}, fmt.Errorf("close fixture %q: %w", entry.Name(), closeErr)
		}
		validationErr := schema.Validate(instance)
		if expectedValid {
			if validationErr != nil {
				return Report{}, fmt.Errorf("valid fixture %q was rejected: %w", entry.Name(), validationErr)
			}
			report.ValidAccepted++
			continue
		}
		if validationErr == nil {
			return Report{}, fmt.Errorf("invalid fixture %q was accepted", entry.Name())
		}
		report.InvalidRejected++
	}
	if report.ValidAccepted == 0 || report.InvalidRejected == 0 {
		return Report{}, fmt.Errorf("fixture coverage requires at least one valid and one invalid fixture")
	}
	return report, nil
}

func compileSchemas(schemaDir string) (map[string]*jsonschema.Schema, error) {
	files := map[string]string{
		"placement": "placement-event.schema.json",
		"group":     "group-result.schema.json",
		"gate":      "gate-decision.schema.json",
	}
	compiled := make(map[string]*jsonschema.Schema, len(files))
	for kind, name := range files {
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		schema, err := compiler.Compile(filepath.Join(schemaDir, name))
		if err != nil {
			return nil, fmt.Errorf("compile %s schema: %w", kind, err)
		}
		compiled[kind] = schema
	}
	return compiled, nil
}

func classifyFixture(name string) (kind string, expectedValid bool, err error) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	switch {
	case strings.HasSuffix(base, "-valid"):
		expectedValid = true
		kind = strings.TrimSuffix(base, "-valid")
	case strings.HasSuffix(base, "-invalid"):
		expectedValid = false
		kind = strings.TrimSuffix(base, "-invalid")
	default:
		return "", false, fmt.Errorf("fixture %q must end in -valid.json or -invalid.json", name)
	}
	return kind, expectedValid, nil
}
