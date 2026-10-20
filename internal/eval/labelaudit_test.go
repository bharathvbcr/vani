package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestLabelAuditScriptedSession(t *testing.T) {
	s, _, textDir := newStore(t)
	recs := []ledger.Record{
		mkRec("gh:a:1", posting.Triaged, nil),
		mkRec("gh:a:2", posting.Dropped, nil),
		mkRec("gh:a:3", posting.Triaged, nil),
		mkRec("gh:a:4", posting.Triaged, nil),
	}
	picks := []AuditPick{
		{Key: "gh:a:1", Source: "decile:10", PSelect: 0.25},
		{Key: "gh:a:2", Source: "dropped:title:role_fit", PSelect: 1},
		{Key: "gh:a:3", Source: "decile:1", PSelect: 0.5},
		{Key: "gh:a:4", Source: "decile:2", PSelect: 0.5},
	}
	// 1 = yes; "9" is not an option and re-prompts; 3 = no; s = skip; then q
	// before the fourth is answered.
	in := strings.NewReader("1\n9\n3\ns\nq\n")
	var out bytes.Buffer
	n, err := LabelAudit(in, &out, picks, recs, s, Names{})
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v\n%s", n, err, out.String())
	}
	got, bad, err := s.Labels()
	if err != nil || bad != 0 || len(got) != 3 {
		t.Fatalf("labels %v bad=%d err=%v", got, bad, err)
	}
	want := map[posting.Key]string{"gh:a:1": "yes", "gh:a:2": "no", "gh:a:3": Skip}
	for k, v := range want {
		e := got[k]
		if e.Labels[WouldApply] != v || e.TextSHA256 != HashText("desc "+string(k)) {
			t.Errorf("%s: %+v", k, e)
		}
	}
	if e := got["gh:a:2"]; e.Source != "dropped:title:role_fit" || e.PSelect != 1 {
		t.Errorf("stratum not recorded: %+v", e)
	}
	if _, has := got["gh:a:4"]; has {
		t.Error("an unanswered posting was written")
	}
	if !strings.Contains(out.String(), "Not an option") || !strings.Contains(out.String(), "Stopped; 3 label(s) written.") {
		t.Errorf("session text:\n%s", out.String())
	}
	if _, err := s.Text(HashText("desc gh:a:1")); err != nil {
		t.Errorf("description not cached by hash under %s: %v", textDir, err)
	}
}

func TestLabelAuditRefusesMismatchedPicks(t *testing.T) {
	s, _, _ := newStore(t)
	recs := []ledger.Record{mkRec("gh:a:1", posting.Triaged, nil)}
	var out bytes.Buffer
	if _, err := LabelAudit(strings.NewReader("1\n"), &out, nil, recs, s, Names{}); err == nil {
		t.Error("length mismatch accepted")
	}
	if _, err := LabelAudit(strings.NewReader("1\n"), &out, []AuditPick{{Key: "gh:b:9", Source: "decile:1", PSelect: 1}}, recs, s, Names{}); err == nil {
		t.Error("pick/posting key mismatch accepted")
	}
	// A pick with no valid p_select is refused by the store, and nothing is written.
	n, err := LabelAudit(strings.NewReader("1\n"), &out, []AuditPick{{Key: "gh:a:1", Source: "decile:1"}}, recs, s, Names{})
	if err == nil || n != 0 {
		t.Errorf("n=%d err=%v", n, err)
	}
	if l, _, _ := s.Labels(); len(l) != 0 {
		t.Errorf("wrote %v", l)
	}
}
