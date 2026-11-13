package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/rankfit"
)

func TestPercentileCountsTiesHalf(t *testing.T) {
	s := []float64{1, 2, 2, 3}
	for v, want := range map[float64]float64{0: 0, 1: 12.5, 2: 50, 3: 87.5, 4: 100} {
		if got := percentile(s, v); got != want {
			t.Errorf("percentile(%v) = %v, want %v", v, got, want)
		}
	}
	if percentile(nil, 1) != 0 {
		t.Error("empty pool")
	}
}

func TestRelevanceReportsTheSeniorityShadow(t *testing.T) {
	auditedState(t)
	o, _ := mustCode(t, 0, "relevance", "--json")
	var r relevanceReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatal(err)
	}
	s := r.SeniorityShadow
	if s == nil || s.Error != "" || len(s.Variants) != len(spareVariants) || s.Pool == 0 {
		t.Fatalf("shadow: %+v", s)
	}
	for _, v := range s.Variants {
		if len(v.AUC) != len(shadowModes) {
			t.Errorf("%s: %d AUC rows", v.Name, len(v.AUC))
		}
		// The seeded postings read strong_fit, never mismatch_seniority: nothing moves.
		if v.Changed != 0 || v.PoolChanged != 0 {
			t.Errorf("%s moved postings it cannot spare: %+v", v.Name, v)
		}
	}
	if text, _ := mustCode(t, 0, "relevance"); !strings.Contains(text, "## Seniority-gate shadow") {
		t.Error("text report lacks the shadow")
	}
}

func TestRankfitSpareSeniorityWritesKeepsAndClears(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	path := filepath.Join(state, "rank", "weights.json")

	o, _ := mustCode(t, 0, "rankfit", "--spare-seniority", "mid,senior", "--dry-run", "--json")
	if !strings.Contains(o, `"written":false`) {
		t.Errorf("dry run: %s", o)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a dry run wrote the file")
	}
	mustCode(t, 0, "rankfit", "--spare-seniority", " mid , senior ")
	f, err := rankfit.Load(path)
	if err != nil || !slices.Equal(f.SpareSeniorityMismatch, []string{"mid", "senior"}) {
		t.Fatalf("written: %+v %v", f.SpareSeniorityMismatch, err)
	}
	// Refused levels never touch the file.
	for _, bad := range []string{"manager", "principal", "mid,mid", ","} {
		mustCode(t, 2, "rankfit", "--spare-seniority", bad)
	}
	if f2, _ := rankfit.Load(path); !slices.Equal(f2.SpareSeniorityMismatch, []string{"mid", "senior"}) {
		t.Errorf("a refused call changed the file: %v", f2.SpareSeniorityMismatch)
	}
	// "none" on a file that holds nothing else would leave it empty: refused.
	mustCode(t, 2, "rankfit", "--spare-seniority", "none")
	// An unreadable file is reported and left alone, never overwritten.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, e := mustCode(t, 1, "rankfit", "--spare-seniority", "senior"); !strings.Contains(e, "left as it is") {
		t.Errorf("unreadable file: %s", e)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Error("the unreadable file was overwritten")
	}
}
