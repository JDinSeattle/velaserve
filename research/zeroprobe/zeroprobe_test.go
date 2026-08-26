package zeroprobe

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JDinSeattle/velaserve/internal/artifacts"
)

func TestPrepareCreatesImmutableManifestBeforeTraffic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "z0")
	manifest, err := Prepare(PrepareOptions{
		Phase:               PhaseZ0A,
		PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"),
		ArtifactRoot:        root,
		CreatedAt:           time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Seed != 20260825 || manifest.PreregistrationSHA256 == "" {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, name := range []string{"manifest.json", "preregistration.yaml", artifacts.LedgerName} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if err := artifacts.Verify(root); err != nil {
		t.Fatalf("artifact ledger verification failed: %v", err)
	}
	if _, err := Prepare(PrepareOptions{
		Phase:               PhaseZ0A,
		PreregistrationPath: filepath.Join("..", "preregistration", "z0-v1.yaml"),
		ArtifactRoot:        root,
		CreatedAt:           time.Now(),
	}); err == nil {
		t.Fatal("second Prepare() overwrote immutable manifest")
	}
}
