package main

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The chunk-vector log keeps vectors by the priority the warm embeds them in, so
// the ranks handed to it are the Score warmOrder sorted by: one computation
// (orderAdvance), read by the warm's order and by the log's eviction.

// rankFixture is four live postings with different Scores (title signals, a
// strong-fit reading, age), one of them (dup) the text of another under a stale
// date.
func rankFixture(t *testing.T) ([]ledger.Record, config.Config) {
	t.Helper()
	cfgPath, _ := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	cfg.TitleSignals.Include = []string{"platform", "engineer"}

	older := semRec("gh:a:older", "Platform Engineer", infraDesc)
	older.Posting.PostedAt = now().AddDate(0, 0, -4)
	older.Annotations = map[string]string{"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered"}
	fresher := semRec("gh:a:fresher", "Accountant", infraDesc+" Fresher.")
	fresher.Posting.PostedAt = now().AddDate(0, 0, -1)
	dup := semRec("gh:b:dup", "Platform Engineer", infraDesc)
	dup.Posting.PostedAt = now().AddDate(0, 0, -60)
	mid := semRec("gh:a:mid", "Platform Engineer", infraDesc+" Mid.")
	mid.Posting.PostedAt = now().AddDate(0, 0, -2)
	recs := []ledger.Record{fresher, dup, mid, older}
	for i := range recs {
		recs[i].Status = posting.Explained // browsable: the Jobs views list it
	}
	return recs, cfg
}

func textHashOf(r ledger.Record) string {
	return semantic.TextHash(r.Posting.Title, r.Posting.Description)
}

func TestWarmRanksAreTheScoreWarmOrderSortedBy(t *testing.T) {
	recs, cfg := rankFixture(t)

	// The Score itself, from the one owner (orderAdvance) over the same records.
	todo := append([]ledger.Record(nil), recs...)
	scores := orderAdvance(todo, recs, nil, warmOrder(cfg)...)
	if len(scores) != len(recs) {
		t.Fatalf("orderAdvance returned %d Scores for %d records", len(scores), len(recs))
	}
	byKey := map[string]ledger.Record{}
	for _, r := range recs {
		byKey[string(r.Key)] = r
	}

	docs, ranks := warmRanked(recs, nil, cfg)
	if len(docs) != len(recs) {
		t.Fatalf("%d docs for %d records", len(docs), len(recs))
	}
	// Every doc's rank is the best Score among the postings with its text.
	best := map[string]float64{}
	for _, r := range recs {
		h := textHashOf(r)
		if s, ok := best[h]; !ok || scores[r.Key] > s {
			best[h] = scores[r.Key]
		}
	}
	if len(ranks) != len(best) {
		t.Fatalf("%d ranks for %d texts: %v", len(ranks), len(best), ranks)
	}
	for h, want := range best {
		if got, ok := ranks[h]; !ok || got != want {
			t.Errorf("text %s ranks %v (present %v), orderAdvance's Score says %v", h[:8], got, ok, want)
		}
	}
	// The warm embeds the highest Score first, and the ranks are those Scores (a
	// text shared by two postings takes the better), so they are not all alike.
	prev := scores[posting.Key(docs[0].Key)]
	distinct := map[float64]bool{}
	for _, d := range docs {
		s := scores[posting.Key(d.Key)]
		if s > prev {
			t.Errorf("%s scores %v after a doc scoring %v: warmOrder is not Score order", d.Key, s, prev)
		}
		prev = s
		distinct[ranks[semantic.TextHash(d.Title, d.Description)]] = true
	}
	if len(distinct) < 3 {
		t.Errorf("ranks %v do not separate the postings", ranks)
	}
	if docs[0].Key != "gh:a:older" {
		t.Errorf("fixture: %s leads, want the strong-fit posting", docs[0].Key)
	}
	// The dup's own Score is lower than its text's rank: the text took the best.
	if dup := byKey["gh:b:dup"]; scores[dup.Key] >= ranks[textHashOf(dup)] {
		t.Errorf("fixture: dup scores %v, its text ranks %v", scores[dup.Key], ranks[textHashOf(dup)])
	}
}

func TestWarmRanksCoverEveryBrowsablePostingAndNoOther(t *testing.T) {
	recs, cfg := rankFixture(t)
	closed := semRec("gh:a:closed", "Closed Engineer", "Operate k8s, closed.")
	closed.Status = posting.Dead
	evicted := semRec("gh:a:evicted", "Evicted Engineer", "")
	evicted.BodyEvicted = true
	all := append(append([]ledger.Record(nil), recs...), closed, evicted)
	docs, ranks := warmRanked(all, nil, cfg)
	if len(docs) != len(recs) {
		t.Fatalf("%d docs, want the %d browsable ones", len(docs), len(recs))
	}
	if _, ok := ranks[textHashOf(closed)]; ok {
		t.Error("a closed posting nobody marked is live in the vector log")
	}
	if _, ok := ranks[textHashOf(evicted)]; ok {
		t.Error("a posting with no resident text is live in the vector log")
	}
	if docs, ranks := warmRanked(nil, nil, cfg); len(docs) != 0 || ranks == nil || len(ranks) != 0 {
		t.Errorf("no records: %d docs, ranks %v", len(docs), ranks)
	}
}

// The idle warm gets the ranks of every browsable posting (scored ones too: they
// are live), best Score first among the backlog; unreadable marks give none.
func TestIdleBacklogHandsTheLogItsRanks(t *testing.T) {
	f := newIdleFixture(t, 0)
	recs, _ := rankFixture(t)
	f.cfg.TitleSignals.Include = []string{"platform", "engineer"}
	for i := range recs {
		r := recs[i]
		f.led.recs[r.Key] = &r
	}
	ctx := context.Background()
	docs, ranks, _, err := f.w.backlog(ctx, f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantDocs, wantRanks := warmRanked(f.led.All(), nil, f.cfg)
	if len(docs) != len(wantDocs) || len(ranks) != len(wantRanks) {
		t.Fatalf("backlog %d docs, %d ranks; warmRanked %d, %d", len(docs), len(ranks), len(wantDocs), len(wantRanks))
	}
	for h, r := range wantRanks {
		if ranks[h] != r {
			t.Errorf("text %s: backlog ranks %v, warmRanked %v", h[:8], ranks[h], r)
		}
	}
	for i := range docs {
		if docs[i].Key != wantDocs[i].Key {
			t.Errorf("backlog doc %d is %s, warmOrder says %s", i, docs[i].Key, wantDocs[i].Key)
		}
	}

	// Scored postings leave the backlog but stay live and ranked.
	if rep := warmSemantic(ctx, f.cfg, f.state, f.led.All(), nil, 100); rep.Scored == 0 {
		t.Fatalf("setup warm: %+v", rep)
	}
	docs, ranks, _, err = f.w.backlog(ctx, f.cfg)
	if err != nil || len(docs) != 0 || len(ranks) != len(wantRanks) {
		t.Errorf("after the warm: %d backlog docs, %d ranks (want 0, %d): %v", len(docs), len(ranks), len(wantRanks), err)
	}

	// Marks that cannot be read name no live set at all.
	corruptMarks(t, marks.Dir(f.state))
	if _, ranks, _, err = f.w.backlog(ctx, f.cfg); err != nil || ranks != nil {
		t.Errorf("unreadable marks: ranks %v, err %v; want none", ranks, err)
	}
}
