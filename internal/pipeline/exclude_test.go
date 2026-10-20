package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// excludeTitle is a stand-in for config.Exclude.Match: titles containing
// "Intern" are excluded.
func excludeTitle(word string) func(posting.Posting) (string, bool) {
	return func(p posting.Posting) (string, bool) {
		if strings.Contains(p.Title, word) {
			return fmt.Sprintf("title matches %q", word), true
		}
		return "", false
	}
}

func excludeRunner(t *testing.T, titles ...string) (*Runner, *fakeTriager, *ledger.File, []posting.Key) {
	t.Helper()
	l := openLedger(t)
	var ps []posting.Posting
	for i, title := range titles {
		p := ghPost(t, fmt.Sprint(i+1), "jd text "+title)
		p.Title = title
		ps = append(ps, p)
	}
	if _, err := Reconcile(ctx, l, board, ps, t0); err != nil {
		t.Fatal(err)
	}
	keys := make([]posting.Key, len(ps))
	for i, p := range ps {
		keys[i] = p.Key
	}
	ft := &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"role_fit": jev("strong_fit", 0.95)}}}
	r := &Runner{Ledger: l, Triager: ft, Scorer: fakeScorer{}, Explainer: fakeExplainer{text: "good fit"},
		Policy: DropPolicy{OpenRouter: "d1-ultra", Exclude: excludeTitle("Intern")}, Badger: fakeBadger{"Acme": "LCA 3"}, Now: func() time.Time { return t0 }}
	return r, ft, l, keys
}

// An excluded posting is dropped at triage with no backend asked, and the
// reason is recorded; others go through the backends as before.
func TestAdvanceDropsExcludedWithoutABackend(t *testing.T) {
	r, ft, l, keys := excludeRunner(t, "Software Engineer Intern", "Software Engineer")
	if err := r.Advance(ctx, keys[0]); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, keys[0])
	if got.Status != posting.Dropped || ft.calls != 0 {
		t.Fatalf("status %s, %d backend call(s)", got.Status, ft.calls)
	}
	if got.Annotations[AnnExcluded] != `excluded: title matches "Intern"` || got.Annotations["triage.backend"] != ExcludeBackend ||
		got.Annotations[ledger.AnnLCA] == "" || !Excluded(got) {
		t.Errorf("annotations %v", got.Annotations)
	}
	if err := r.Advance(ctx, keys[1]); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, keys[1]); got.Status != posting.Explained || ft.calls != 1 || got.Annotations[AnnExcluded] != "" {
		t.Fatalf("kept posting: %s, %d call(s), %v", got.Status, ft.calls, got.Annotations)
	}
}

// Exclusions win over title readings: the reason is the exclusion's.
func TestEvaluatePostingExclusionFirst(t *testing.T) {
	p := DropPolicy{Rules: titleRules, ReadTitle: readsOn(map[string]TitleReading{"Staff": {Slot: "seniority", Value: "staff_plus"}}),
		Exclude: excludeTitle("Intern")}
	if a, why, ok := p.EvaluatePosting(posting.Posting{Title: "Staff Intern"}); !ok || a.Backend != ExcludeBackend || !strings.HasPrefix(why, "excluded: ") {
		t.Errorf("(%v, %q, %v)", a, why, ok)
	}
	if a, _, ok := p.EvaluatePosting(posting.Posting{Title: "Staff Engineer"}); !ok || a.Backend != TitleBackend {
		t.Errorf("title reading: (%v, %v)", a, ok)
	}
	if _, _, ok := p.EvaluatePosting(posting.Posting{Title: "Engineer"}); ok {
		t.Error("dropped a posting nothing matches")
	}
	if (DropPolicy{}).Prefilters() || !(DropPolicy{Exclude: excludeTitle("x")}).Prefilters() {
		t.Error("Prefilters")
	}
}

// The run's pass drops kept postings an exclusion now matches (never a
// marked one, never a dead or duplicate one), reopens the ones it no longer
// matches, and a second pass changes nothing.
func TestApplyExclusionsDropsAndReopens(t *testing.T) {
	r, ft, l, keys := excludeRunner(t, "Engineer Intern", "Data Intern", "Engineer", "Marked Intern", "Gone Intern")
	// Everything is advanced before the owner adds the exclusion.
	r.Policy.Exclude = nil
	for _, k := range keys {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: keys[4], Status: posting.Dead, Reason: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkDigested(ctx, keys[1:2]); err != nil {
		t.Fatal(err)
	}
	calls := ft.calls
	marked := func(k posting.Key) bool { return k == keys[3] }
	p := DropPolicy{Exclude: excludeTitle("Intern")}

	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), p, marked, nil)
	if err != nil || len(ch.Drop) != 2 || len(ch.Reopen) != 0 {
		t.Fatalf("first pass: %+v (%v)", ch, err)
	}
	for _, k := range keys[:2] {
		got := rec(t, l, k)
		if !Excluded(got) || got.Annotations["triage.role_fit"] != "strong_fit" {
			t.Errorf("%s: %s %v (the model's reading is kept as evidence)", k, got.Status, got.Annotations)
		}
	}
	if rec(t, l, keys[2]).Status != posting.Explained || rec(t, l, keys[3]).Status != posting.Explained || rec(t, l, keys[4]).Status != posting.Dead {
		t.Error("the pass touched a posting it must not")
	}
	if ft.calls != calls {
		t.Error("the pass asked a backend")
	}

	ch, err = ApplyExclusions(ctx, l, t0.Add(2*time.Hour), p, marked, nil)
	if err != nil || len(ch.Drop)+len(ch.Reopen) != 0 {
		t.Fatalf("second pass not a no-op: %+v (%v)", ch, err)
	}

	// The owner narrows the exclusion: "Data Intern" no longer matches and
	// returns to live with its triage forgotten and its badge kept.
	p.Exclude = excludeTitle("Engineer Intern")
	ch, err = ApplyExclusions(ctx, l, t0.Add(3*time.Hour), p, marked, nil)
	if err != nil || len(ch.Reopen) != 1 || ch.Reopen[0].Key != keys[1] || len(ch.Drop) != 0 {
		t.Fatalf("narrowed: %+v (%v)", ch, err)
	}
	got := rec(t, l, keys[1])
	if got.Status != posting.Live || got.Annotations[AnnExcluded] != "" || got.Annotations["triage.role_fit"] != "" ||
		got.Annotations[ledger.AnnRank] != "" || got.Annotations[ledger.AnnLCA] == "" {
		t.Errorf("reopened: %s %v", got.Status, got.Annotations)
	}

	// With no exclusions at all, every excluded posting comes back.
	ch, err = ApplyExclusions(ctx, l, t0.Add(4*time.Hour), DropPolicy{}, marked, nil)
	if err != nil || len(ch.Reopen) != 1 || ch.Reopen[0].Key != keys[0] {
		t.Fatalf("cleared: %+v (%v)", ch, err)
	}
}

// A posting dropped by a title reading or a model is not an exclusion's to
// reopen.
func TestApplyExclusionsLeavesOtherDrops(t *testing.T) {
	r, _, l, k := titleRunner(t, "Staff Software Engineer", titleRules)
	if err := r.Advance(ctx, k); err != nil || rec(t, l, k).Status != posting.Dropped {
		t.Fatal("setup")
	}
	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), DropPolicy{}, nil, nil)
	if err != nil || len(ch.Reopen)+len(ch.Drop) != 0 || rec(t, l, k).Status != posting.Dropped {
		t.Fatalf("%+v (%v)", ch, err)
	}
}

// More postings than one batch carries are all dropped, in several writes.
func TestApplyExclusionsBatches(t *testing.T) {
	titles := make([]string, exclusionBatch+7)
	for i := range titles {
		titles[i] = fmt.Sprintf("Intern %d", i)
	}
	_, _, l, keys := excludeRunner(t, titles...)
	ch, err := ApplyExclusions(ctx, l, t0.Add(time.Hour), DropPolicy{Exclude: excludeTitle("Intern")}, nil, nil)
	if err != nil || len(ch.Drop) != len(titles) {
		t.Fatalf("%d dropped (%v)", len(ch.Drop), err)
	}
	for _, k := range keys {
		if !Excluded(rec(t, l, k)) {
			t.Fatalf("%s not dropped", k)
		}
	}
}

func TestApplyExclusionsStopsOnCancel(t *testing.T) {
	_, _, l, keys := excludeRunner(t, "Intern")
	c, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ApplyExclusions(c, l, t0, DropPolicy{Exclude: excludeTitle("Intern")}, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if rec(t, l, keys[0]).Status != posting.Live {
		t.Error("a cancelled pass wrote")
	}
}

// A posting reopened by hand (retriage) that an exclusion still matches has
// its stale reason cleared only when triage keeps it; triage drops it again.
func TestAdvanceClearsAStaleExclusion(t *testing.T) {
	r, _, l, keys := excludeRunner(t, "Engineer")
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: keys[0], Status: posting.Live,
		Annotations: map[string]string{AnnExcluded: "excluded: old"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Advance(ctx, keys[0]); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, keys[0]); got.Status != posting.Explained || got.Annotations[AnnExcluded] != "" {
		t.Fatalf("%s %v", got.Status, got.Annotations)
	}
}
