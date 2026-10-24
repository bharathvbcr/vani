package rkbresume

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Red-team tests of continuation handling. The real-bank test is read-only and
// opt-in (VANI_REDTEAM_REALBANK=1) and logs bullet IDS and the NAME of the
// pattern that fired, never bullet text.

const (
	rtRealBank      = "/Users/bharath/Code/career/CurriculumVitae/resume/bank.json"
	rtRealSelection = "/Users/bharath/Code/career/CurriculumVitae/resume/resumes"
)

// rtBackRef is a deliberately broad net for text that refers back to an earlier
// bullet. Every hit is a candidate false negative of isContinuation.
var rtBackRef = map[string]*regexp.Regexp{
	"those/these":      regexp.MustCompile(`(?i)\b(?:those|these)\b`),
	"the same":         regexp.MustCompile(`(?i)\bthe same\b`),
	"that/this-noun":   regexp.MustCompile(`(?i)\b(?:of|from|in|on|with|for|to|by) (?:that|this)\b`),
	"above/former":     regexp.MustCompile(`(?i)\b(?:above|aforementioned|former|latter|previous|earlier|prior work|same)\b`),
	"it-with":          regexp.MustCompile(`(?i)\b[a-z]+(?:ed) (?:it|them) (?:with|to|in|on|across)\b`),
	"also/again":       regexp.MustCompile(`(?i)^[\s*]*(?:\*\*[^*]*\*\*[:\s]*)?(?:also|again|then|further|likewise|similarly)\b`),
	"pronoun-start":    regexp.MustCompile(`(?i)^[\s*]*(?:it|they|this|that|these|those|its|their)\b`),
	"pronoun-in-lead":  regexp.MustCompile(`(?i)^\*\*[^*]*\b(?:it|they|them|its|their|this|that|those|these)\b[^*]*\*\*`),
	"as-before":        regexp.MustCompile(`(?i)\b(?:as before|as above|same as|the other|both of|each of them)\b`),
	"second/another-x": regexp.MustCompile(`(?i)\b(?:a second|the second|another) (?:gpu|vendor|machine|platform|system|model|team|company)\b`),
}

func TestRedteamContinuationDetectorOverTheRealBank(t *testing.T) {
	if os.Getenv("VANI_REDTEAM_REALBANK") != "1" {
		t.Skip("set VANI_REDTEAM_REALBANK=1 to run against the real bank (read-only; logs ids only)")
	}
	bank, err := kbsync.Load(rtRealBank)
	if err != nil {
		t.Skipf("no real bank: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(rtRealSelection, "*.json"))
	sort.Strings(files)
	var sels []kbsync.Selection
	for _, f := range files {
		s, err := kbsync.LoadSelection(f)
		if err != nil {
			t.Logf("selection not loadable (skipped): %s", filepath.Base(f))
			continue
		}
		sels = append(sels, s)
	}
	t.Logf("bank bullets=%d selections loaded=%d", len(bank.Bullets), len(sels))

	cont := buildContinuations(sels, func(id string) (string, bool) { b, ok := bank.Bullets[id]; return b.Text, ok })
	detected := map[string]bool{}
	for id, b := range bank.Bullets {
		if isContinuation(b.Text) {
			detected[id] = true
		}
	}
	var ids []string
	for id := range detected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	t.Logf("detected continuations (%d): %s", len(ids), strings.Join(ids, ", "))

	// Detected, listed in a selection, but with no predecessor under any entry
	// that lists it first: it is never offered to the model.
	var noPred []string
	for entry, m := range cont {
		for id, pred := range m {
			if pred == "" {
				noPred = append(noPred, id+"@"+entry)
			}
		}
	}
	sort.Strings(noPred)
	t.Logf("continuations that come first in some selection list (never offered under that entry) (%d): %s", len(noPred), strings.Join(noPred, ", "))

	// Where each detected continuation sits: entry@predecessor, and whether a
	// second selection lists it under the same entry after a different bullet.
	for _, id := range ids {
		var where []string
		for entry, m := range cont {
			if pred, ok := m[id]; ok {
				where = append(where, entry+"<-"+pred)
			}
		}
		sort.Strings(where)
		if len(where) == 0 {
			t.Logf("continuation %s is in no selection (never offered)", id)
			continue
		}
		t.Logf("continuation %s: %s", id, strings.Join(where, " | "))
	}

	// Candidates the detector missed.
	hits := map[string][]string{}
	for id, b := range bank.Bullets {
		if detected[id] {
			continue
		}
		for name, re := range rtBackRef {
			if re.MatchString(b.Text) {
				hits[name] = append(hits[name], id)
			}
		}
	}
	var names []string
	for n := range hits {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		sort.Strings(hits[n])
		t.Logf("candidate false negatives by pattern %q (%d): %s", n, len(hits[n]), strings.Join(hits[n], ", "))
	}

	// Position sensitivity: a bullet whose bold lead precedes the back-reference
	// still reads as a continuation, but the anchored patterns only see the start.
	var anchored []string
	for id, b := range bank.Bullets {
		if detected[id] {
			continue
		}
		after := b.Text
		if i := strings.Index(after, ":**"); i >= 0 {
			after = after[i+3:]
		}
		if regexp.MustCompile(`(?i)^\s*(?:\{\{clm[A-Za-z]+\}\}|[0-9][0-9.,]*[%x]?) of that\b`).MatchString(after) ||
			regexp.MustCompile(`^\s*[A-Z][a-z]+ed it with\b`).MatchString(after) {
			anchored = append(anchored, id)
		}
	}
	sort.Strings(anchored)
	if len(anchored) > 0 {
		t.Errorf("anchored continuation phrases that follow a bold lead are missed by the detector (%d): %s", len(anchored), strings.Join(anchored, ", "))
	}
}

// A detected continuation survives only with the predecessor the owner wrote it
// after, under the same entry. Attack the places that rule could be bypassed.

func rtContBuilder(t *testing.T) (*fixture, *builder, pool) {
	t.Helper()
	f := newFixture(t)
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatalf("load: %v", err)
	}
	return f, b, b.buildPool(func(string) bool { return true })
}

// The same continuation listed under two entries has a predecessor per entry;
// a1-style entries must not borrow each other's.
func TestRedteamContinuationUnderAnotherEntryNeedsThatEntrysPredecessor(t *testing.T) {
	f := newFixture(t)
	// c3 is "Brought the same lessons ...". Bank: let exp-b list it too, and the
	// owner's selection puts it first under exp-b but after c2 under exp-c.
	editJSON(t, f.bank, func(m map[string]any) {
		ents := m["entries"].(map[string]any)
		eb := ents["exp-b"].(map[string]any)
		eb["bullets"] = []any{"b1", "b2", "c3"}
	})
	editJSON(t, f.other, func(m map[string]any) {
		secs := m["sections"].([]any)
		exp := secs[1].(map[string]any)
		ents := exp["entries"].([]any)
		ents[1] = map[string]any{"entry": "exp-b", "bullets": []string{"c3", "b2"}} // c3 first under exp-b
	})
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := b.buildPool(func(string) bool { return true })
	if p.has("exp-b", "c3") {
		t.Fatalf("a continuation that comes first under exp-b is offered under exp-b")
	}
	// The model smuggles it in anyway (it is not in the pool under exp-b).
	reply := replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1","c3"]},{"entry":"exp-c","bullets":["c1"]}]}`)
	if _, err := b.parseChoice(reply, p, testJD); err == nil {
		t.Fatalf("a continuation with no predecessor under exp-b was accepted under exp-b")
	}
}

// A chain A -> B -> C (B and C are continuations): dropping B must take C.
func TestRedteamChainDropsEveryLinkAfterTheDroppedOne(t *testing.T) {
	f := newFixture(t)
	editJSON(t, f.bank, func(m map[string]any) {
		bl := m["bullets"].(map[string]any)
		mk := func(text string) map[string]any { return map[string]any{"text": text, "review": reviewFor(text)} }
		bl["k1"] = mk("Built the scheduler for GPU jobs")
		bl["k2"] = mk("Operated the systems beneath those features and {{clmUnv}} faster")
		bl["k3"] = mk("Brought the same lessons to the whole fleet again")
		ents := m["entries"].(map[string]any)
		ec := ents["exp-c"].(map[string]any)
		ec["bullets"] = append(ec["bullets"].([]any), "k1", "k2", "k3")
	})
	editJSON(t, f.other, func(m map[string]any) {
		secs := m["sections"].([]any)
		ents := secs[1].(map[string]any)["entries"].([]any)
		ents[2] = map[string]any{"entry": "exp-c", "bullets": []string{"c1", "k1", "k2", "k3"}}
	})
	// Unverified only while k2 is on the page, so the pool offers it (see
	// TestRedteamDroppingAnUnverifiedBulletDoesNotDeleteAJob).
	f.rkb.unverified = map[string][]string{"clmUnv": {"k2"}}
	f.provider.replies = []string{replyWith(secSummary,
		`{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["c1","k1","k2","k3"]}]}`)}
	res, err := f.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Selection != SelectionTailored || !has(res.UnverifiedDropped, "k2") {
		t.Fatalf("the scenario did not run: selection=%s dropped=%v reason=%q", res.Selection, res.UnverifiedDropped, res.Reason)
	}
	last := f.rkb.sels[len(f.rkb.sels)-1]
	if has(selBullets(t, last), "k3") {
		t.Fatalf("k3 (continues k2) shipped after k2 was dropped; dropped=%v", res.UnverifiedDropped)
	}
	if !has(selBullets(t, last), "k1") {
		t.Fatalf("k1, which nothing depends on, was lost with the chain")
	}
}
