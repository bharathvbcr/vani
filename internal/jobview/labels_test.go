package jobview

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/jobcategory"
)

const labelsGoldenPath = "testdata/labels.golden.json"

func labelsJSON(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Vocabulary()); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The golden file is what the Swift (VaniApplyKit) and extension tests replay.
func TestLabelsGolden(t *testing.T) {
	want := labelsJSON(t)
	if *update {
		if err := os.WriteFile(labelsGoldenPath, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(labelsGoldenPath)
	if err != nil {
		t.Fatalf("%v (go test ./internal/jobview -update writes it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run go test ./internal/jobview -update and commit it", labelsGoldenPath)
	}
}

// Every vocabulary covers its source of truth exactly, in the source's order,
// with a non-empty distinct label: a new triage option or category without
// words fails here, not on a screen.
func TestVocabularyCoversItsSources(t *testing.T) {
	v := Vocabulary()
	if v.V != LabelsVersion {
		t.Fatalf("v = %d", v.V)
	}
	check := func(name string, got []LabelEntry, want []string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d entries, want %d", name, len(got), len(want))
		}
		seen := map[string]bool{}
		for i, e := range got {
			if e.Value != want[i] {
				t.Errorf("%s[%d] = %q, want %q", name, i, e.Value, want[i])
			}
			if e.Label == "" || e.Label == e.Value || seen[e.Label] {
				t.Errorf("%s: %q has label %q", name, e.Value, e.Label)
			}
			seen[e.Label] = true
		}
	}
	check("fit", v.Fit, FitOrder[:])
	check("category", v.Category, jobcategory.OptionValues())
	check("status", v.Status, StatusOrder[:])
	n := 0
	for _, s := range jdtriage.Slots {
		if s.Name == "role_fit" || s.Name == "category" {
			continue
		}
		var vals []string
		for _, o := range s.Options {
			vals = append(vals, o.Value)
		}
		check("triage."+s.Name, v.Triage[s.Name], vals)
		n++
	}
	if n == 0 || len(v.Triage) != n {
		t.Fatalf("triage slots %d, vocabulary has %d", n, len(v.Triage))
	}
}

func TestVocabularyIsACopy(t *testing.T) {
	v := Vocabulary()
	v.Fit[0].Label = "changed"
	v.Triage["work_mode"][0].Label = "changed"
	if w := Vocabulary(); w.Fit[0].Label == "changed" || w.Triage["work_mode"][0].Label == "changed" {
		t.Fatal("Vocabulary shares state with its caller")
	}
}
