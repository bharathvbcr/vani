package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// sentence i is eight words no other sentence shares and none a sensitive stem.
func sentence(i int) string {
	return fmt.Sprintf("Sentence%d covers topic alpha%d beta%d gamma%d delta%d.", i, i, i, i, i)
}

// jdWith is 40 plain sentences (about 320 words) with mid inserted after the
// twentieth, so a test can vary exactly one sentence.
func jdWith(mid string) string {
	var b strings.Builder
	for i := range 40 {
		if i == 20 && mid != "" {
			b.WriteString(mid + " ")
		}
		b.WriteString(sentence(i) + " ")
	}
	return b.String()
}

func editPost(desc string) posting.Posting {
	return posting.Posting{Title: "Software Engineer", Location: "Remote", Company: "Acme", Description: desc}
}

func TestKeepAcrossEdit(t *testing.T) {
	plain := jdWith("")
	replaced := strings.Replace(plain, sentence(7), "A different sentence now sits here about something else.", 1)
	rewritten := func() string {
		var b strings.Builder
		for i := range 40 {
			if i%3 == 0 {
				fmt.Fprintf(&b, "Rewritten line %d says entirely new things about wording%d and phrasing%d here. ", i, i, i)
			} else {
				b.WriteString(sentence(i) + " ")
			}
		}
		return b.String()
	}()
	over := strings.Repeat("filler word ", EditKeepMaxBytes/12+10)
	clearance := "Active security clearance is required for this position."
	cases := []struct {
		name     string
		old, cur posting.Posting
		keep     bool
	}{
		{"one sentence edited", editPost(plain), editPost(replaced), true},
		{"identical text", editPost(plain), editPost(plain), true},
		{"h-1b respelled", editPost(jdWith("We accept H-1B candidates.")), editPost(jdWith("We accept H1B candidates.")), true},
		{"sponsor line added", editPost(plain), editPost(plain + "We do not sponsor visas."), false},
		{"clearance sentence removed", editPost(jdWith(clearance)), editPost(plain), false},
		{"will sponsor to will not sponsor", editPost(jdWith("We will sponsor visas for strong candidates.")),
			editPost(jdWith("We will not sponsor visas for strong candidates.")), false},
		{"remote to hybrid", editPost(jdWith("This role is remote across the country.")),
			editPost(jdWith("This role is hybrid across the country.")), false},
		{"on-site added", editPost(plain), editPost(plain + "Candidates work on-site daily."), false},
		{"3+ years to 8+ years", editPost(jdWith("You bring 3+ years of Go experience.")),
			editPost(jdWith("You bring 8+ years of Go experience.")), false},
		{"paragraph rewritten", editPost(plain), editPost(rewritten), false},
		{"stub", editPost(words("stub", 20)), editPost(words("stub", 20) + "x"), false},
		{"old over the cap", editPost(over), editPost(plain), false},
		{"new over the cap", editPost(plain), editPost(over), false},
		{"both over the cap", editPost(over), editPost(over + "z"), false},
		{"title changed", editPost(plain), func() posting.Posting { p := editPost(plain); p.Title = "Senior Software Engineer"; return p }(), false},
		{"location changed", editPost(plain), func() posting.Posting { p := editPost(plain); p.Location = "Austin, TX"; return p }(), false},
		{"pay line is not board text", editPost(replaced), editPost("Pay range: USD 1 – 2 per year\n\n" + plain), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			score, keep := KeepAcrossEdit(c.old, c.cur)
			if keep != c.keep {
				t.Fatalf("keep = %v (score %q), want %v", keep, score, c.keep)
			}
			if keep && score == "" {
				t.Fatal("a kept edit has no score")
			}
		})
	}
}

// triaged files p, advances it to Explained and gives it derived annotations.
func triaged(t *testing.T, l ledger.Ledger, p posting.Posting) {
	t.Helper()
	if s := reconcileOne(t, l, board, t0, p); s.New != 1 {
		t.Fatalf("seed: %+v", s)
	}
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Explained, Annotations: map[string]string{
		"triage.remote": "yes", "triage.backend": "jev", "score.total": "5", "gap.coverage_pct": "9",
		"explanation": "e", "other": "keep"}}); err != nil {
		t.Fatal(err)
	}
}

func keptJob(t *testing.T, desc string) posting.Posting {
	p := ghPost(t, "1", desc)
	p.Title = "Software Engineer"
	return p
}

func TestReconcileKeepsTriageAcrossANearIdenticalEdit(t *testing.T) {
	l := openLedger(t)
	plain := jdWith("")
	p := keptJob(t, plain)
	triaged(t, l, p)

	edited := p
	edited.Description = strings.Replace(plain, sentence(7), "A different sentence now sits here about something else.", 1)
	s := reconcileOne(t, l, board, t0.Add(time.Hour), edited)
	if s.Kept != 1 || s.Changed != 0 {
		t.Fatalf("summary %+v, want one Kept and no Changed", s)
	}
	r := rec(t, l, p.Key)
	if r.Status != posting.Explained {
		t.Fatalf("status %s, want explained", r.Status)
	}
	for k, want := range map[string]string{"triage.remote": "yes", "triage.backend": "jev", "score.total": "5",
		"gap.coverage_pct": "9", "explanation": "e", "other": "keep", DescHashKey: edited.DescriptionHash()} {
		if r.Annotations[k] != want {
			t.Errorf("annotation %s = %q, want %q", k, r.Annotations[k], want)
		}
	}
	if score := r.Annotations[ledger.AnnTriageKept]; score < "0.90" || score > "1.00" {
		t.Errorf("kept score %q", score)
	}
	if r.Posting.Description != edited.Description {
		t.Error("the stored posting did not take the edited text")
	}

	// Idempotent: the same listing again is unchanged.
	if s := reconcileOne(t, l, board, t0.Add(2*time.Hour), edited); s.Unchanged != 1 || s.Kept != 0 || s.Changed != 0 {
		t.Fatalf("rerun %+v", s)
	}

	// A real change forgets the kept note with the rest of the triage.
	rewritten := p
	rewritten.Description = words("brandnew", 200)
	if s := reconcileOne(t, l, board, t0.Add(3*time.Hour), rewritten); s.Changed != 1 || s.Kept != 0 {
		t.Fatalf("rewrite %+v", s)
	}
	r = rec(t, l, p.Key)
	if r.Status != posting.Live {
		t.Fatalf("status %s, want live", r.Status)
	}
	if _, ok := r.Annotations[ledger.AnnTriageKept]; ok {
		t.Error("the kept note survived a real change")
	}
	forgot := r.Annotations["triage.remote"] == "" && r.Annotations["score.total"] == ""
	if !forgot {
		t.Errorf("derived annotations survived: %v", r.Annotations)
	}
}

func TestReconcileReasksWhenAnEditTouchesASensitiveSentence(t *testing.T) {
	for name, edit := range map[string]string{
		"visa line added":    "We do not sponsor visas.",
		"clearance added":    "Applicants need an active clearance.",
		"hybrid line added":  "The team works hybrid from our office.",
		"seniority modified": "You bring 8+ years of experience.",
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			plain := jdWith("")
			p := keptJob(t, plain)
			triaged(t, l, p)
			edited := p
			edited.Description = plain + edit
			s := reconcileOne(t, l, board, t0.Add(time.Hour), edited)
			if s.Changed != 1 || s.Kept != 0 {
				t.Fatalf("summary %+v, want a plain change", s)
			}
			r := rec(t, l, p.Key)
			if r.Status != posting.Live || r.Annotations["triage.remote"] != "" {
				t.Fatalf("not re-asked: %s %v", r.Status, r.Annotations)
			}
		})
	}
}

func TestReconcileRerenderIsNotAKeptEdit(t *testing.T) {
	l := openLedger(t)
	plain := jdWith("")
	p := keptJob(t, plain)
	triaged(t, l, p)
	re := p
	re.Description = "Pay range: USD 1 – 2 per year\n\n" + plain
	s := reconcileOne(t, l, board, t0.Add(time.Hour), re)
	if s.Rerendered != 1 || s.Kept != 0 || s.Changed != 0 {
		t.Fatalf("summary %+v", s)
	}
	if _, ok := rec(t, l, p.Key).Annotations[ledger.AnnTriageKept]; ok {
		t.Error("a re-render wrote the kept note")
	}
}

// A revived duplicate whose text was edited a little keeps its triage, and one
// whose text was rewritten loses it.
func TestReviveKeepsTriageAcrossANearIdenticalEdit(t *testing.T) {
	plain := jdWith("")
	edits := map[string]struct {
		desc string
		keep bool
	}{
		"small edit": {strings.Replace(plain, sentence(7), "A different sentence now sits here about something else.", 1), true},
		"rewrite":    {words("brandnew", 200), false},
	}
	for name, c := range edits {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			gh := ghJob(t, "1", "Staff Engineer", "Remote", "")
			lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
			gh.Description, lv.Description = plain, plain
			reconcileOne(t, l, board, t0, gh)
			reconcileOne(t, l, lvBoard, t0, lv)
			wantDuplicate(t, l, lv.Key, gh.Key)
			if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: lv.Key, Status: posting.Duplicate,
				Annotations: map[string]string{"triage.remote": "yes", "score.total": "5"}}); err != nil {
				t.Fatal(err)
			}
			edited := lv
			edited.Description = c.desc
			var s Summary
			for i := 1; i <= MissesToDead; i++ {
				reconcileOne(t, l, board, reviveDay(i)) // the survivor is no longer listed
				listed := lv
				if i == MissesToDead {
					listed = edited
				}
				s = reconcileOne(t, l, lvBoard, reviveDay(i), listed)
			}
			if s.Revived != 1 {
				t.Fatalf("summary %+v", s)
			}
			r := rec(t, l, lv.Key)
			_, kept := r.Annotations[ledger.AnnTriageKept]
			if kept != c.keep || (r.Annotations["triage.remote"] == "yes") != c.keep || (r.Annotations["score.total"] == "5") != c.keep {
				t.Fatalf("keep = %v, annotations %v", c.keep, r.Annotations)
			}
			if r.Status != posting.Live {
				t.Fatalf("status %s, want live", r.Status)
			}
		})
	}
}

func editSentence(desc string, i int) string {
	return strings.Replace(desc, sentence(i), fmt.Sprintf("Replacement line number %d says something unrelated entirely.", i), 1)
}

// Kept edits are bounded: three in a row keep, the fourth re-asks and forgets
// the count and the basis, and a later triage starts counting again.
func TestKeptEditsAreBoundedAndTheCountResets(t *testing.T) {
	l := openLedger(t)
	desc := jdWith("")
	p := keptJob(t, desc)
	triaged(t, l, p)
	basis := rec(t, l, p.Key).Annotations[DescHashKey]

	cur := p
	for n := 1; n <= EditKeepMaxEdits; n++ {
		desc = editSentence(desc, 5*n)
		cur.Description = desc
		if s := reconcileOne(t, l, board, t0.Add(time.Duration(n)*time.Hour), cur); s.Kept != 1 {
			t.Fatalf("edit %d: %+v, want kept", n, s)
		}
		a := rec(t, l, p.Key).Annotations
		if a[ledger.AnnTriageKeptEdits] != fmt.Sprint(n) || a[ledger.AnnTriageBasisHash] != basis || a["triage.remote"] != "yes" {
			t.Fatalf("edit %d: annotations %v", n, a)
		}
	}
	desc = editSentence(desc, 30)
	cur.Description = desc
	if s := reconcileOne(t, l, board, t0.Add(10*time.Hour), cur); s.Kept != 0 || s.Changed != 1 {
		t.Fatalf("fourth edit: %+v, want a re-ask", s)
	}
	r := rec(t, l, p.Key)
	for _, k := range []string{ledger.AnnTriageKeptEdits, ledger.AnnTriageBasisHash, ledger.AnnTriageKept, "triage.remote"} {
		if _, ok := r.Annotations[k]; ok {
			t.Fatalf("%s survived the re-ask: %v", k, r.Annotations)
		}
	}

	// Triage reads the new text; the next small edit is the first kept one again.
	if err := l.Append(ctx, ledger.Event{V: 1, At: t0.Add(11 * time.Hour), Key: p.Key, Status: posting.Explained,
		Annotations: map[string]string{"triage.remote": "yes"}}); err != nil {
		t.Fatal(err)
	}
	newBasis := rec(t, l, p.Key).Annotations[DescHashKey]
	desc = editSentence(desc, 35)
	cur.Description = desc
	if s := reconcileOne(t, l, board, t0.Add(12*time.Hour), cur); s.Kept != 1 {
		t.Fatalf("after reset: %+v", s)
	}
	a := rec(t, l, p.Key).Annotations
	if a[ledger.AnnTriageKeptEdits] != "1" || a[ledger.AnnTriageBasisHash] != newBasis {
		t.Fatalf("after reset: %v", a)
	}
}

// A revive applies the same bound.
func TestReviveHonoursTheKeptEditBound(t *testing.T) {
	plain := jdWith("")
	for name, c := range map[string]struct {
		edits string
		keep  bool
		want  string
	}{"two kept so far": {"2", true, "3"}, "three kept so far": {"3", false, ""}} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			gh := ghJob(t, "1", "Staff Engineer", "Remote", "")
			lv := lvJob(t, "a1", "Staff Engineer", "Remote", "")
			gh.Description, lv.Description = plain, plain
			reconcileOne(t, l, board, t0, gh)
			reconcileOne(t, l, lvBoard, t0, lv)
			if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: lv.Key, Status: posting.Duplicate,
				Annotations: map[string]string{"triage.remote": "yes", ledger.AnnTriageKeptEdits: c.edits, ledger.AnnTriageBasisHash: "h0"}}); err != nil {
				t.Fatal(err)
			}
			edited := lv
			edited.Description = editSentence(plain, 7)
			for i := 1; i <= MissesToDead; i++ {
				reconcileOne(t, l, board, reviveDay(i))
				listed := lv
				if i == MissesToDead {
					listed = edited
				}
				reconcileOne(t, l, lvBoard, reviveDay(i), listed)
			}
			a := rec(t, l, lv.Key).Annotations
			if a[ledger.AnnTriageKeptEdits] != c.want || (a["triage.remote"] == "yes") != c.keep {
				t.Fatalf("annotations %v", a)
			}
			if c.keep && a[ledger.AnnTriageBasisHash] != "h0" {
				t.Fatalf("basis replaced: %v", a)
			}
		})
	}
}
