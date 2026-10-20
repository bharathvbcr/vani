package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// inheritedCopy is a record whose triage reading was copied from another
// posting (triage.inherited_from): it is the original's reading, not a second
// observation, so it must not add to any count.
func inheritedCopy(key, from string, r ledger.Record) ledger.Record {
	c := mkRec(key, r.Status, map[string]string{})
	for k, v := range r.Annotations {
		c.Annotations[k] = v
	}
	c.Annotations[ledger.AnnTriageInheritedFrom] = from
	return c
}

func TestReportExcludesInheritedReadings(t *testing.T) {
	s, _, _ := newStore(t)
	for _, k := range []string{"A", "A2", "A3"} {
		if err := s.Put(Entry{Key: posting.Key(k), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
	}
	a := jevRec("A", "work_mode", "remote", "0.9700", "remote", true)
	recs := []ledger.Record{a, inheritedCopy("A2", "A", a), inheritedCopy("A3", "A", a)}
	rep := BuildReport(recs, s, Names{})
	if len(rep.Cells) != 1 || rep.Cells[0].Labeled != 1 || rep.Cells[0].Correct != 1 {
		t.Fatalf("copies inflated n: %+v", rep.Cells)
	}
	for _, th := range rep.Thresholds {
		for _, row := range th.Rows {
			if row.Labeled > 1 {
				t.Fatalf("threshold row counts copies: %+v", row)
			}
		}
	}
	for _, sh := range rep.Shadow {
		if sh.Labeled > 1 {
			t.Fatalf("shadow row counts copies: %+v", sh)
		}
	}
	if rep.Matched != 1 || rep.WithTriage != 1 || rep.Inherited != 2 {
		t.Fatalf("header: matched %d with-triage %d inherited %d", rep.Matched, rep.WithTriage, rep.Inherited)
	}
	if md := rep.Markdown(); !strings.Contains(md, "inherited") {
		t.Fatalf("the report does not say copies were left out:\n%s", md)
	}
}

func TestQueueSkipsInheritedReadings(t *testing.T) {
	s, _, _ := newStore(t)
	tri := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered"}
	orig := mkRec("orig", posting.Triaged, tri)
	recs := []ledger.Record{orig, inheritedCopy("copy", "orig", orig),
		inheritedCopy("copy-unsure", "orig", mkRec("u", posting.Unsure, map[string]string{"triage.unsure": "x"}))}
	queue, err := Queue(recs, s, 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(queue); strings.Join(got, ",") != "orig" {
		t.Fatalf("queue offered a copy for labelling: %v", got)
	}
}
