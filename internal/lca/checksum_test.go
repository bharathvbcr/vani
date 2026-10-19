package lca

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldSave writes a file the way Save did before the checksum was taken from
// the raw bytes: sources marshalled twice, once for the sum and once inside
// the document. Files already on disk look like this.
func oldSave(t *testing.T, path string, srcs []fileSource) {
	t.Helper()
	b, err := json.Marshal(srcs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Version  int          `json:"version"`
		Checksum string       `json:"checksum"`
		Sources  []fileSource `json:"sources"`
	}{SchemaVersion, sumOf(b), srcs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func oneSource() []fileSource {
	return []fileSource{{
		SourceMeta: SourceMeta{Name: "a.csv", SHA256: "aa", RowsRead: 2, RowsCounted: 2},
		Employers:  map[string]map[int]Counts{"ACME <&> CO": {2024: {H1B: 2}}, "BETA": {2023: {E3: 1}, 2025: {H1B1: 1}}},
	}}
}

func TestIndexWrittenByTheOldSaveStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	oldSave(t, path, oneSource())
	x, err := Load(path)
	if err != nil {
		t.Fatalf("an index the old Save wrote: %v", err)
	}
	if x.Employers() != 2 {
		t.Fatalf("%d employers", x.Employers())
	}
	// And Save now writes the same bytes the old one did.
	again := filepath.Join(t.TempDir(), "again.json")
	if err := x.Save(again); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(path)
	b, _ := os.ReadFile(again)
	if !bytes.Equal(a, b) {
		t.Fatalf("Save changed the file format:\nold %s\nnew %s", a, b)
	}
}

// The checksum is of the bytes on disk. A sources block whose values are the
// same but whose spacing is not used to pass (Load re-encoded it); now it is
// refused like any other change.
func TestRespacedSourcesFailTheChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	oldSave(t, path, oneSource())
	data, _ := os.ReadFile(path)
	respaced := strings.Replace(string(data), `"sources":[{`, `"sources":[ {`, 1)
	if respaced == string(data) {
		t.Fatal("fixture did not change")
	}
	if err := os.WriteFile(path, []byte(respaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Load: %v, want a checksum mismatch", err)
	}
}

func TestTamperedSourcesFailTheChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	oldSave(t, path, oneSource())
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(data, []byte(`[2,0,0,0]`), []byte(`[9,0,0,0]`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Load: %v, want a checksum mismatch", err)
	}
}

// An unknown field inside a source is still refused: the inner decode keeps
// DisallowUnknownFields.
func TestUnknownFieldInsideSourcesIsRefused(t *testing.T) {
	raw := []byte(`[{"name":"a.csv","sha256":"aa","rows_read":0,"rows_counted":0,"employers":{},"extra":1}]`)
	data, _ := json.Marshal(fileDoc{Version: SchemaVersion, Checksum: sumOf(raw), Sources: raw})
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "corrupt index") {
		t.Fatalf("Load: %v, want a corrupt index", err)
	}
}
