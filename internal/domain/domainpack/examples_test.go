package domainpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// The presales / FDE / operations examples live under docs/ so they never enter
// the embedded registry. They still have to parse and activate cleanly: this
// test is the contract behind the documented "copy and rename" path.
func TestDocumentedExamplesAreValidManifests(t *testing.T) {
	const dir = "../../../docs/domainpacks/examples"

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}

	want := map[string]bool{
		"solution-presales": false,
		"fde":               false,
		"ops-growth":        false,
	}

	seen := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".example.yaml") {
			continue
		}
		seen++
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		pack, err := parse(data)
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		wantID := strings.TrimSuffix(entry.Name(), ".example.yaml")
		if pack.ID != wantID {
			t.Fatalf("%s: id %q must match file name %q", entry.Name(), pack.ID, wantID)
		}
		if _, ok := want[pack.ID]; !ok {
			t.Fatalf("%s: unexpected example id %q", entry.Name(), pack.ID)
		}
		want[pack.ID] = true

		// Mirrors the documented activation: place the file at
		// domainpacks/<id>/manifest.yaml and the loader accepts it.
		fsys := fstest.MapFS{
			pack.ID + "/manifest.yaml": &fstest.MapFile{Data: data},
		}
		registry, err := LoadFS(fsys)
		if err != nil {
			t.Fatalf("%s: activation failed: %v", entry.Name(), err)
		}
		if _, ok := registry.Get(pack.ID); !ok {
			t.Fatalf("%s: activated pack %q not registered", entry.Name(), pack.ID)
		}
	}
	if seen != len(want) {
		t.Fatalf("examples = %d, want %d", seen, len(want))
	}
	for id, found := range want {
		if !found {
			t.Fatalf("missing example for %q", id)
		}
	}
}

// Examples must stay outside the embedded registry until someone activates one.
func TestExamplesAreNotEmbedded(t *testing.T) {
	const dir = "../../../docs/domainpacks/examples"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read examples dir: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".example.yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		pack, err := parse(data)
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if _, ok := Default().Get(pack.ID); ok {
			t.Fatalf("example %q must not be embedded", pack.ID)
		}
	}
}
