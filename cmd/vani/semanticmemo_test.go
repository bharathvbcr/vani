package main

import (
	"context"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// The insight's remembered match is the matcher's answer for the posting as it
// is now: the rank and the row agree with semanticScore, and a posting whose
// text changed under the same key is looked up again (and, its score being for
// other text, not found), never handed the old match.
func TestInsightSemanticMatchFollowsThePostingText(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	cfg := loadCfg(t, cfgPath)
	recs := []ledger.Record{
		semRec("gh:a:1-sales", "Engineer", salesDesc),
		semRec("gh:a:2-infra", "Engineer", infraDesc),
	}
	if rep := warmSemanticDocs(context.Background(), cfg, state, docsOf(recs), 10); rep.Scored != 2 {
		t.Fatalf("warm %+v", rep)
	}
	in := &insight{}
	in.attachSemantic(cfg, state)
	if in.sem == nil || in.rc.Semantic == nil {
		t.Fatalf("no matcher after the warm: %q", in.info.Warning)
	}
	for _, r := range recs {
		want, wantOK := semanticScore(in.sem, r)
		for range 3 { // first ask, then remembered
			got, ok := in.semanticMatch(r)
			if ok != wantOK || got.Score != want.Score || got.Thin != want.Thin || len(got.Evidence) != len(want.Evidence) {
				t.Fatalf("%s: memo %+v %v, matcher %+v %v", r.Key, got, ok, want, wantOK)
			}
			score, rankOK := in.rc.Semantic(r)
			if score != want.Score || rankOK != (wantOK && !want.Thin) {
				t.Fatalf("%s: rank %v %v, want %v %v", r.Key, score, rankOK, want.Score, wantOK && !want.Thin)
			}
			var j jobview.Job
			in.semanticInto(&j, r)
			if (j.Semantic != nil) != wantOK || (j.Semantic != nil && j.Semantic.Score != want.Score) {
				t.Fatalf("%s: row %+v, want %+v %v", r.Key, j.Semantic, want, wantOK)
			}
		}
	}

	changed := recs[1]
	changed.Posting.Description = salesDesc // same key, other text
	if _, ok := in.semanticMatch(changed); ok {
		t.Error("a changed description was served the match scored for the old text")
	}
	retitled := recs[1]
	retitled.Posting.Title = "Staff Engineer"
	if _, ok := in.semanticMatch(retitled); ok {
		t.Error("a changed title was served the match scored for the old text")
	}
	if _, ok := in.semanticMatch(recs[1]); !ok {
		t.Error("the original text is no longer found after a changed one was asked")
	}

	// Concurrent asks (the daily run ranks in parallel) agree and do not race.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i := range 100 {
				r := recs[i%2]
				if i%5 == 0 {
					r = changed
				}
				want, wantOK := semanticScore(in.sem, r)
				if got, ok := in.semanticMatch(r); ok != wantOK || got.Score != want.Score {
					t.Errorf("concurrent %s: %v %v, want %v %v", r.Key, got.Score, ok, want.Score, wantOK)
					return
				}
			}
		})
	}
	wg.Wait()
}

// With semantic matching off there is no matcher: nothing is ranked by it and
// no row shows a match, as before the memo.
func TestInsightSemanticMatchWithoutMatcher(t *testing.T) {
	in := &insight{}
	r := semRec("gh:a:1", "Engineer", infraDesc)
	if _, ok := in.semanticMatch(r); ok {
		t.Error("a match without a matcher")
	}
	var j jobview.Job
	in.semanticInto(&j, r)
	if j.Semantic != nil {
		t.Errorf("row match without a matcher: %+v", j.Semantic)
	}
}
