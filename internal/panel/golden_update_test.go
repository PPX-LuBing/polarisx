package panel

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestUpdateGolden is an opt-in Go-side snapshot writer. It reads the frozen
// input database/settings, runs the current Go generators, and writes the
// resulting JSON snapshots. UPDATE_GOLDEN=1 is the only update entry point.
func TestUpdateGolden(t *testing.T) {
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		return
	}
	root := filepath.Join("..", "..", "testdata")
	golden := filepath.Join(root, "golden")
	repoAbs, err := filepath.Abs(filepath.Join(root, ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixtureRoot, err := filepath.Abs(filepath.Join(root, "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("POLARISX_HOME", fixtureRoot)

	// Xray config snapshots are generated directly from the Go config builder.
	store := openFixture(t, root)
	defer store.Close()
	settingsFiles, err := filepath.Glob(filepath.Join(golden, "*.settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(settingsFiles)
	for _, settingsPath := range settingsFiles {
		name := strings.TrimSuffix(filepath.Base(settingsPath), ".settings.json")
		if name == "" || name == "with-cc-asn" {
			continue
		}
		var st Settings
		raw, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("%s: %v", settingsPath, err)
		}
		cfg, err := store.BuildConfig(st)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		writeGoldenJSON(t, filepath.Join(golden, name+".json"), normalizeGoldenPaths(cfg, repoAbs))
	}

	// Other snapshots contain multiple protocol payloads and HTTP cases. They
	// are already produced by the same Go APIs in their dedicated golden tests;
	// canonicalizing them here makes the update command deterministic and keeps
	// formatting stable across Go versions.
	files, err := filepath.Glob(filepath.Join(golden, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for _, name := range files {
		if strings.HasSuffix(name, ".settings.json") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("%s: invalid JSON: %v", name, err)
		}
		writeGoldenJSON(t, name, value)
	}
}

func normalizeGoldenPaths(v any, repo string) any {
	b, _ := json.Marshal(v)
	s := strings.ReplaceAll(string(b), repo, "{REPO}")
	var out any
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func writeGoldenJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	old, _ := os.ReadFile(path)
	if bytes.Equal(old, data) {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("updated %s", path)
}
