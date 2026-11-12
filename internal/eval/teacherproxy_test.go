package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestTeacherDropProxyCountsCandidateMisses(t *testing.T) {
	rec := func(k string, st posting.Status, ann map[string]string) ledger.Record {
		return ledger.Record{Key: posting.Key(k), Status: st, Annotations: ann}
	}
	model := map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain"}
	recs := []ledger.Record{
		rec("gh:a:1", posting.Dropped, model),
		rec("gh:a:2", posting.Dropped, model),
		rec("gh:a:3", posting.Dropped, model),
		rec("gh:a:3", posting.Dropped, model), // listed twice: once
		rec("gh:a:4", posting.Dropped, map[string]string{"triage.backend": "title", "triage.seniority": "manager"}),
		rec("gh:a:5", posting.Digested, model), // kept: not a drop
		rec("gh:a:6", posting.Dropped, map[string]string{"triage.backend": "exclude", AnnExcludeMatched: `company is "x"`}),
	}
	teacher := fakeTeacher{
		"gh:a:1": {"role_fit": "partial_fit"},
		"gh:a:2": {"role_fit": "mismatch_domain"},
		"gh:a:3": {"seniority": "mid"}, // read, but not role_fit: says nothing
		"gh:a:5": {"role_fit": "strong_fit"},
	}
	p := BuildTeacherDropProxy(recs, classRules, teacher)
	want := map[string]TeacherDropRow{
		"dropped:model:role_fit":  {Class: "dropped:model:role_fit", Dropped: 3, Read: 2, Positive: 1},
		"dropped:title:seniority": {Class: "dropped:title:seniority", Dropped: 1},
		"dropped:exclude:company": {Class: "dropped:exclude:company", Dropped: 1},
	}
	if len(p.Rows) != len(want) || !p.Available {
		t.Fatalf("rows %+v", p.Rows)
	}
	for _, r := range p.Rows {
		if r != want[r.Class] {
			t.Errorf("%s: %+v, want %+v", r.Class, r, want[r.Class])
		}
	}
	md := p.Markdown()
	if !strings.Contains(md, "1/2 = 50%") || !strings.Contains(md, "| dropped:title:seniority | 1 | 0 | no readings |") || !strings.Contains(md, "not recall") {
		t.Errorf("markdown:\n%s", md)
	}
	// No teacher: not available, never a table of zeros.
	none := BuildTeacherDropProxy(recs, classRules, nil)
	if none.Available || len(none.Rows) != 0 || !strings.Contains(none.Markdown(), "not available") {
		t.Errorf("no teacher: %+v", none)
	}
	if empty := BuildTeacherDropProxy(nil, nil, teacher); !strings.Contains(empty.Markdown(), "no dropped postings") {
		t.Errorf("no drops: %s", empty.Markdown())
	}
}
