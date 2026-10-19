package employer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oldSave writes the index file the way Save did before the checksum was
// taken from the raw bytes. Files already on disk look like this.
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
		SourceMeta: SourceMeta{Name: "hd.csv", Kind: KindIPEDS, SHA256: "aa"},
		Names:      map[string]attr{"UNIVERSITY OF EXAMPLE": bitCapExempt | bitNonprofit, "SAINT ANNE FOUNDATION": bitNonprofit},
	}}
}

func TestIndexWrittenByTheOldSaveStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	oldSave(t, path, oneSource())
	x, err := Load(path)
	if err != nil {
		t.Fatalf("an index the old Save wrote: %v", err)
	}
	if b := x.Lookup("University of Example"); b.Confidence != ConfExact {
		t.Fatalf("lookup after load: %+v", b)
	}
	again := filepath.Join(dir, "again.json")
	if err := x.Save(again); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(path)
	b, _ := os.ReadFile(again)
	if !bytes.Equal(a, b) {
		t.Fatalf("Save changed the file format:\nold %s\nnew %s", a, b)
	}
}

// The checksum is of the bytes on disk: re-spacing the sources block used to
// pass (Load re-encoded it) and is now refused like any other change.
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

func TestUnknownFieldInsideSourcesIsRefused(t *testing.T) {
	raw := []byte(`[{"name":"hd.csv","kind":"ipeds","sha256":"aa","names":{},"extra":1}]`)
	data, _ := json.Marshal(fileDoc{Version: SchemaVersion, Checksum: sumOf(raw), Sources: raw})
	path := filepath.Join(t.TempDir(), "index.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "corrupt index") {
		t.Fatalf("Load: %v, want a corrupt index", err)
	}
}
