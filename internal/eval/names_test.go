package eval

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// renameFixture builds 40 labeled postings over two slots. Primary backends:
// 0-19 legacyName, 20-34 "d1-ultra", 35-37 "llm", 38-39 none recorded. Most
// carry a "local" shadow; posting 5's shadow is recorded as "jev", which must
// stay "jev" (only the primary is ever mapped).
func renameFixture(t *testing.T, legacyName string) ([]ledger.Record, *Store) {
	t.Helper()
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("R%02d", i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote", "seniority": "mid"}}); err != nil {
			t.Fatal(err)
		}
		ann := map[string]string{}
		for _, slot := range []string{"work_mode", "seniority"} {
			pre := "triage." + slot
			backend := legacyName
			switch {
			case i >= 38:
				backend = ""
			case i >= 35:
				backend = "llm"
			case i >= 20:
				backend = "d1-ultra"
			}
			if backend != "" {
				ann[pre+".backend"] = backend
			}
			ann[pre+".reading"] = "answered"
			ann[pre] = map[string]string{"work_mode": "remote", "seniority": "mid"}[slot]
			if i%7 == 0 {
				ann[pre] = "senior" // wrong for both slots
			}
			if backend != "llm" && backend != "" {
				ann[pre+".p"] = fmt.Sprintf("0.%02d00", 50+i)
			}
			if i%4 != 3 {
				ann[pre+".shadow_reading"] = "answered"
				ann[pre+".shadow_backend"] = "local"
				ann[pre+".shadow"] = ann[pre]
				if i == 5 {
					ann[pre+".shadow_backend"] = "jev"
				}
				if i%5 == 0 {
					ann[pre+".shadow"] = "onsite"
				}
			}
		}
		recs = append(recs, mkRec(key, posting.Triaged, ann))
	}
	return recs, s
}

// The rename must not move a number: a ledger mixing pre-rename "jev" rows
// with "d1-ultra" rows reports exactly what the same ledger reports with
// every primary already named "d1-ultra".
func TestLegacyPrimaryNameReportsIdentically(t *testing.T) {
	names := Names{OpenRouter: "d1-ultra"}
	mixedRecs, mixedStore := renameFixture(t, LegacyOpenRouterName)
	renamedRecs, renamedStore := renameFixture(t, "d1-ultra")
	mixed := BuildReport(mixedRecs, mixedStore, names)
	renamed := BuildReport(renamedRecs, renamedStore, names)

	if mixed.LegacyPrimary != 40 || renamed.LegacyPrimary != 0 {
		t.Fatalf("legacy primary counts: mixed %d (want 20 postings x 2 slots), renamed %d", mixed.LegacyPrimary, renamed.LegacyPrimary)
	}
	for name, pair := range map[string][2]any{
		"cells":      {mixed.Cells, renamed.Cells},
		"thresholds": {mixed.Thresholds, renamed.Thresholds},
		"shadow":     {mixed.Shadow, renamed.Shadow},
		"promotions": {mixed.Promotions, renamed.Promotions},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s differ:\nmixed   %+v\nrenamed %+v", name, pair[0], pair[1])
		}
	}
	legacyLine := fmt.Sprintf("- primary readings recorded under the old name %q and counted as %q: 40\n", "jev", "d1-ultra")
	md := mixed.Markdown()
	if !strings.Contains(md, legacyLine) {
		t.Fatalf("mixed report does not say how many readings it mapped:\n%s", md)
	}
	if got, want := strings.Replace(md, legacyLine, "", 1), renamed.Markdown(); got != want {
		t.Fatalf("markdown differs beyond the legacy line:\n--- mixed\n%s\n--- renamed\n%s", got, want)
	}

	for _, c := range mixed.Cells {
		if c.Backend == LegacyOpenRouterName {
			t.Fatalf("a primary cell is still filed as %q: %+v", LegacyOpenRouterName, c)
		}
	}
	if len(mixed.Thresholds) == 0 || !strings.Contains(md, "## d1-ultra precision at p threshold") {
		t.Fatalf("threshold tables missing or misnamed:\n%s", md)
	}
}

// Only a slot's primary backend is mapped: the shadow's recorded name, and
// the llm and unrecorded primaries, are read as written.
func TestLegacyAliasLeavesShadowAndOtherBackends(t *testing.T) {
	recs, s := renameFixture(t, LegacyOpenRouterName)
	rep := BuildReport(recs, s, Names{OpenRouter: "d1-ultra"})

	shadows := map[string]bool{}
	primaries := map[string]bool{}
	for _, p := range rep.Promotions {
		shadows[p.Shadow] = true
		primaries[p.Primary] = true
	}
	if !shadows["jev"] || !shadows["local"] {
		t.Fatalf("shadow names were rewritten; got %v, want jev and local kept", shadows)
	}
	if primaries["jev"] {
		t.Fatalf("a promotion row still has primary jev: %+v", rep.Promotions)
	}
	byBackend := map[string]int{}
	for _, c := range rep.Cells {
		if c.Slot == "work_mode" {
			byBackend[c.Backend] = c.Labeled
		}
	}
	want := map[string]int{"d1-ultra": 35, "llm": 3, UnknownBackend: 2}
	if !reflect.DeepEqual(byBackend, want) {
		t.Fatalf("work_mode cells by backend %v, want %v", byBackend, want)
	}
}

// With no OpenRouter model configured nothing is mapped: legacy rows report
// as "jev", as they did before the rename.
func TestNoConfiguredNameKeepsLegacyRows(t *testing.T) {
	recs, s := renameFixture(t, LegacyOpenRouterName)
	rep := BuildReport(recs, s, Names{})
	if rep.LegacyPrimary != 0 {
		t.Fatalf("mapped %d readings with no configured name", rep.LegacyPrimary)
	}
	got := map[string]bool{}
	for _, c := range rep.Cells {
		got[c.Backend] = true
	}
	if !got["jev"] || !got["d1-ultra"] {
		t.Fatalf("cells %v: want jev and d1-ultra kept apart", got)
	}
	if rep.ProbabilityBackend != "jev" {
		t.Fatalf("threshold tables built from %q, want jev", rep.ProbabilityBackend)
	}
}

// The labelling screen shows the mapped name, never the legacy one, for the
// primary, and leaves the shadow's own name alone.
func TestLabelShowsMappedPrimaryName(t *testing.T) {
	r := mkRec("k", posting.Triaged, map[string]string{
		"triage.work_mode": "remote", "triage.work_mode.reading": "answered",
		"triage.work_mode.backend": "jev", "triage.work_mode.p": "0.9100",
	})
	line := triageLine(r, "work_mode", Names{OpenRouter: "d1-ultra"})
	if !strings.Contains(line, "d1-ultra") || strings.Contains(line, "jev") {
		t.Fatalf("line %q", line)
	}
	if line := triageLine(r, "work_mode", Names{}); !strings.Contains(line, "jev") {
		t.Fatalf("unconfigured name rewritten: %q", line)
	}
}
