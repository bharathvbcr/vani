package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// readsOn returns a title reader that reads each listed slot value whenever
// the title contains its phrase (a stand-in for config.TitleSignals.Reads).
func readsOn(reads map[string]TitleReading) func(string) []TitleReading {
	return func(title string) []TitleReading {
		var out []TitleReading
		for phrase, r := range reads {
			if strings.Contains(title, phrase) {
				r.Matched = phrase
				out = append(out, r)
			}
		}
		return out
	}
}

var titleRules = []DropRule{
	{Slot: "seniority", Values: []string{"staff_plus", "manager"}, MinOpenRouterProb: 0.9},
	{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9},
}

func TestEvaluateTitle(t *testing.T) {
	reader := readsOn(map[string]TitleReading{
		"Staff":   {Slot: "seniority", Value: "staff_plus"},
		"Manager": {Slot: "seniority", Value: "manager"},
		"Sales":   {Slot: "role_fit", Value: "mismatch_domain"},
		"Remote":  {Slot: "work_mode", Value: "remote"},
	})
	p := DropPolicy{Rules: titleRules, OpenRouter: "d1-ultra", ReadTitle: reader}
	for title, want := range map[string]string{
		"Staff Software Engineer": `slot "seniority" = "staff_plus": title matched "Staff"`,
		"Engineering Manager":     `slot "seniority" = "manager": title matched "Manager"`,
		"Sales Lead":              `slot "role_fit" = "mismatch_domain": title matched "Sales"`,
		"Software Engineer":       "", // no reading
		"Remote Engineer":         "", // read, but no rule drops it
		"":                        "",
	} {
		a, reason, dropped := p.EvaluateTitle(title)
		if dropped != (want != "") || reason != want {
			t.Errorf("%q: dropped %v reason %q, want %q", title, dropped, reason, want)
		}
		if !dropped && (a.Backend != "" || a.Slots != nil) {
			t.Errorf("%q: a kept title returned an answer %+v; it must be discarded", title, a)
		}
		if dropped && a.Backend != TitleBackend {
			t.Errorf("%q: backend %q", title, a.Backend)
		}
	}
	// A dropped title records every slot it read, not only the dropping one.
	a, _, _ := p.EvaluateTitle("Staff Sales Remote")
	if len(a.Slots) != 3 || a.Slots["work_mode"].Value != "remote" || a.Slots["role_fit"].Reading != decide.Answered {
		t.Fatalf("answer %+v", a)
	}

	// The owner keeps managers: no rule names manager, so the model reads it.
	keep := DropPolicy{Rules: []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}, MinOpenRouterProb: 0.9}}, ReadTitle: reader}
	if _, _, dropped := keep.EvaluateTitle("Engineering Manager"); dropped {
		t.Fatal("Engineering Manager dropped although no rule drops manager")
	}
	// A rule with zero thresholds still drops on a title reading: the
	// reading is deterministic, and the rule's presence is the owner's call.
	zero := DropPolicy{Rules: []DropRule{{Slot: "seniority", Values: []string{"manager"}}}, ReadTitle: reader}
	if _, _, dropped := zero.EvaluateTitle("Engineering Manager"); !dropped {
		t.Fatal("rule without thresholds did not drop a title reading")
	}
	// No reader, or no rules: nothing drops, nothing is unsure.
	for name, q := range map[string]DropPolicy{"no reader": {Rules: titleRules}, "no rules": {ReadTitle: reader}} {
		if a, r, dropped := q.EvaluateTitle("Staff Sales Manager"); dropped || r != "" || a.Slots != nil {
			t.Errorf("%s: %+v %q %v", name, a, r, dropped)
		}
	}
}

func titleRunner(t *testing.T, title string, rules []DropRule) (*Runner, *fakeTriager, *ledger.File, posting.Key) {
	t.Helper()
	l := openLedger(t)
	p := ghPost(t, "1", "jd text")
	p.Title = title
	if _, err := Reconcile(ctx, l, board, []posting.Posting{p}, t0); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{
		"seniority": jev("mid", 0.95), "role_fit": jev("strong_fit", 0.95)}}}
	r := &Runner{Ledger: l, Triager: ft, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "good fit"},
		Policy: DropPolicy{Rules: rules, OpenRouter: "d1-ultra", ReadTitle: readsOn(map[string]TitleReading{
			"Staff":   {Slot: "seniority", Value: "staff_plus"},
			"Manager": {Slot: "seniority", Value: "manager"},
		})},
		Badger: fakeBadger{"Acme": "LCA: 3 filings"},
		Now:    func() time.Time { return t0.Add(time.Minute) }}
	return r, ft, l, p.Key
}

// reasons records each appended event's reason.
type reasons struct {
	*ledger.File
	got []string
}

func (r *reasons) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	for _, e := range evs {
		r.got = append(r.got, e.Reason)
	}
	return r.File.AppendBatch(ctx, evs)
}

func (r *reasons) Append(ctx context.Context, e ledger.Event) error {
	r.got = append(r.got, e.Reason)
	return r.File.Append(ctx, e)
}

func TestAdvanceTitleDropNeverAsksTheBackends(t *testing.T) {
	r, ft, l, k := titleRunner(t, "Staff Software Engineer", titleRules)
	rl := &reasons{File: l}
	r.Ledger = rl
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 0 {
		t.Fatalf("triager called %d time(s) for a title the rules drop", ft.calls)
	}
	got := rec(t, l, k)
	if got.Status != posting.Dropped || len(rl.got) != 1 || rl.got[0] != `slot "seniority" = "staff_plus": title matched "Staff"` {
		t.Fatalf("status %s reasons %q", got.Status, rl.got)
	}
	a := got.Annotations
	for key, want := range map[string]string{
		"triage.backend": "title", "triage.seniority": "staff_plus", "triage.seniority.reading": "answered",
		"triage.seniority.backend": "title", ledger.AnnLCA: "LCA: 3 filings",
	} {
		if a[key] != want {
			t.Errorf("%s = %q, want %q", key, a[key], want)
		}
	}
	if a["triage.role_fit"] != "" || a["triage.unsure"] != "" || a[ledger.AnnRank] != "" {
		t.Errorf("unexpected annotations %v", a)
	}
	// Terminal: a second advance asks nothing.
	if err := r.Advance(ctx, k); err != nil || ft.calls != 0 {
		t.Fatalf("second advance: %v, %d calls", err, ft.calls)
	}
}

func TestAdvanceKeptTitleIsReadByTheBackends(t *testing.T) {
	// The owner keeps managers: the title reads manager, no rule drops it, so
	// the model reads the posting and only its answer is recorded.
	rules := []DropRule{{Slot: "seniority", Values: []string{"staff_plus"}, MinOpenRouterProb: 0.9}}
	r, ft, l, k := titleRunner(t, "Engineering Manager", rules)
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	if ft.calls != 1 {
		t.Fatalf("triager called %d times", ft.calls)
	}
	got := rec(t, l, k)
	if got.Status != posting.Explained || got.Annotations["triage.backend"] != "d1-ultra" ||
		got.Annotations["triage.seniority"] != "mid" || got.Annotations["triage.seniority.backend"] != "d1-ultra" {
		t.Fatalf("status %s annotations %v", got.Status, got.Annotations)
	}
}

func TestAdvanceTitleDropStillNeedsATriager(t *testing.T) {
	// A missing stage stays a loud configuration error, whatever the title.
	r, _, _, k := titleRunner(t, "Staff Software Engineer", titleRules)
	r.Triager = nil
	if err := r.Advance(ctx, k); err == nil || !strings.Contains(err.Error(), "triager") {
		t.Fatalf("err %v", err)
	}
}
