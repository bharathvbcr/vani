package main

import (
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// Attack 3a. The shadow is asked only about the non-promoted slots, but its
// slotFilter hands the router a NotAsked reading for the promoted ones, and the
// router tags every shadow reading. Recorded, that is
// triage.<promoted>.shadow_reading=not_asked / .shadow_backend=lappi: a shadow
// "asked" about a slot it never ran, beside a primary that is also lappi.
func TestAttackShadowLeavesNoRecordOnPromotedSlot(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	rt, _, shadowL, _, _ := partialPromotionRouter(promoted)
	rec := advanceThrough(t, rt, "gh:attack:1")
	for _, a := range shadowL.asked {
		if a == promoted[0] {
			t.Fatalf("shadow was asked the promoted slot")
		}
	}
	pre := ledger.AnnTriagePrefix + promoted[0]
	if got := rec.Annotations[pre+".backend"]; got != "lappi" {
		t.Errorf("promoted slot primary backend %q, want lappi", got)
	}
	for _, suf := range []string{ledger.AnnTriageSuffixShadowReading, ledger.AnnTriageSuffixShadowBackend} {
		if v, has := rec.Annotations[pre+suf]; has {
			t.Errorf("promoted slot %s carries %s=%q though no shadow ran it", promoted[0], suf, v)
		}
	}
	// The slots still shadowed are recorded normally.
	other := ledger.AnnTriagePrefix + jdtriage.Families[1].Slot.Name
	if rec.Annotations[other+ledger.AnnTriageSuffixShadowBackend] != "lappi" || rec.Annotations[other+ledger.AnnTriageSuffixShadowReading] != "answered" {
		t.Errorf("non-promoted slot lost its shadow record: %v", rec.Annotations)
	}
}

// Attack 3b. The promotion report must not gain a row for a promoted slot from
// a shadow that no longer runs it (and must never pair lappi against lappi).
func TestAttackPromotionReportHasNoSelfPairedRowForPromotedSlot(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	rt, _, _, _, _ := partialPromotionRouter(promoted)
	rec := advanceThrough(t, rt, "gh:attack:2")

	s, err := eval.NewStore(filepath.Join(t.TempDir(), "labels"), filepath.Join(t.TempDir(), "text"))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, sl := range jdtriage.Slots {
		labels[sl.Name] = sl.Options[0].Value
	}
	if err := s.Put(eval.Entry{Key: rec.Posting.Key, Labels: labels}); err != nil {
		t.Fatal(err)
	}
	rep := eval.BuildReport([]ledger.Record{rec}, s, eval.Names{OpenRouter: "d1-ultra"})
	for _, p := range rep.Promotions {
		if p.Slot == promoted[0] {
			t.Errorf("report has a promotion row for the promoted slot: shadow=%s primary=%s asked=%d shadowAnswered=%d verdict=%q",
				p.Shadow, p.Primary, p.Asked, p.ShadowAnswered, p.Verdict())
		}
	}
	for _, sr := range rep.Shadow {
		if sr.Slot == promoted[0] {
			t.Errorf("report has a shadow row for the promoted slot: %+v", sr)
		}
	}
}
