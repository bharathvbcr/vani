package eval

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// floorWorld labels n postings on work_mode (all "remote"), the registered
// split's size, so every one is held out. The first skipped carry the label
// "skip". Of the rest, shadowOnly are right only for the shadow, primaryOnly
// only for the primary, and every other posting both are right on.
func floorWorld(t *testing.T, n, skipped, shadowOnly, primaryOnly int) (HeldOut, Report) {
	t.Helper()
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("gh:co:%02d", i)
		label := "remote"
		if i < skipped {
			label = Skip
		}
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": label}}); err != nil {
			t.Fatal(err)
		}
		got, sh := "remote", "remote"
		switch k := i - skipped; {
		case k >= 0 && k < shadowOnly:
			got = "onsite"
		case k >= shadowOnly && k < shadowOnly+primaryOnly:
			sh = "onsite"
		}
		pre := "triage.work_mode"
		recs = append(recs, mkRec(key, posting.Triaged, map[string]string{
			pre: got, pre + ".reading": "answered", pre + ".backend": "d1-ultra", pre + ".p": "0.9",
			pre + ".shadow": sh, pre + ".shadow_reading": "answered", pre + ".shadow_backend": "local",
		}))
	}
	all, h, err := BuildHeldOut(recs, s, Names{OpenRouter: "d1-ultra"}, registered(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Promotions) != 1 {
		t.Fatalf("promotion rows %+v", h.Promotions)
	}
	return h, all
}

// A3-3: 30 held out, all asked and paired, 12 discordant, the shadow clearly
// better: promotable. One skip must not undo that for the held-out table, and
// must still hold the all-labels table back (its gate is unchanged).
func TestHeldOutPromotableAtFullSplit(t *testing.T) {
	h, all := floorWorld(t, 30, 0, 12, 0)
	p := h.Promotions[0]
	if p.Asked != 30 || p.Paired != 30 || p.Discordant() != 12 {
		t.Fatalf("counts %+v", p)
	}
	if v := h.Verdict(p); v != "promotable" {
		t.Fatalf("held-out verdict %q", v)
	}
	if v := all.Promotions[0].Verdict(); v != "promotable" {
		t.Fatalf("all-labels verdict %q", v)
	}
	if why := h.Insufficiency(p); why != "" {
		t.Fatalf("a promotable row explains an insufficiency: %q", why)
	}

	// Three skips: 27 asked. The held-out table still gives its verdict; the
	// all-labels gate, which is unchanged, does not.
	h3, all3 := floorWorld(t, 30, 3, 12, 0)
	p3 := h3.Promotions[0]
	if p3.Asked != 27 || h3.Verdict(p3) != "promotable" {
		t.Fatalf("27 asked: %+v verdict %q", p3, h3.Verdict(p3))
	}
	if v := all3.Promotions[0].Verdict(); v != "insufficient labels (27/30)" {
		t.Fatalf("all-labels verdict %q, want the unchanged 30-label gate", v)
	}
	if md := h3.Markdown(); !strings.Contains(md, "| promotable |") {
		t.Errorf("held-out markdown does not show the verdict:\n%s", md)
	}
}

// 25 labelled: insufficient, and the report says why and what size would do.
func TestHeldOutInsufficientSaysWhyAndWhatWouldSuffice(t *testing.T) {
	h, _ := floorWorld(t, 25, 0, 12, 0)
	p := h.Promotions[0]
	if v := h.Verdict(p); v != "insufficient labels (25/27)" {
		t.Fatalf("verdict %q", v)
	}
	md := h.Markdown()
	for _, want := range []string{
		"insufficient labels (25/27)",
		"floors scale to the split: at least 27 of the 30 held-out postings asked, and 27 paired (3 skips allowed)",
		"25 asked, needs 27",
		"label or record readings for 2 more held-out postings",
		"a split of size 28 or less would be cleared by the 25 asked",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

// The boundaries, on counts: asked and paired 26/27, discordant 9/10, around
// the held-out floor, and the unchanged all-labels gate at 29/30.
func TestHeldOutFloorBoundaries(t *testing.T) {
	h := HeldOut{Split: registered(t), HeldOut: make([]posting.Key, 30)}
	if f := h.Split.PromotionFloor(); f != 27 {
		t.Fatalf("floor %d, want 30 less %d skips", f, HeldOutAllowedSkips)
	}
	row := func(asked, paired, shadowOnly, primaryOnly int) Promotion {
		return Promotion{Slot: "s", Shadow: "x", Primary: "p", Asked: asked, Paired: paired,
			ShadowAnswered: paired, PrimaryAnswered: paired, ShadowOnly: shadowOnly, PrimaryOnly: primaryOnly}
	}
	for _, tc := range []struct {
		name string
		p    Promotion
		want string // held-out verdict
		why  string // a fragment of the explanation, "" for none
	}{
		{"asked 26", row(26, 26, 12, 0), "insufficient labels (26/27)", "26 asked, needs 27"},
		{"asked 27", row(27, 27, 12, 0), "promotable", ""},
		{"asked 29", row(29, 29, 12, 0), "promotable", ""},
		{"asked 30", row(30, 30, 12, 0), "promotable", ""},
		{"paired 26", row(30, 26, 12, 0), "insufficient paired labels (26/27)", "26 paired, needs 27"},
		{"paired 27", row(30, 27, 12, 0), "promotable", ""},
		{"paired 29", row(30, 29, 12, 0), "promotable", ""},
		{"discordant 9", row(30, 30, 9, 0), "insufficient disagreements (9/10)", "9 discordant, needs 10"},
		{"discordant 10", row(30, 30, 10, 0), "promotable", ""},
		{"discordant 10 split 5/5", row(30, 30, 5, 5), "not promotable: shadow right in 5 of 10 disagreements, lower bound 0.22 < 0.40", ""},
	} {
		if got := h.Verdict(tc.p); got != tc.want {
			t.Errorf("%s: verdict %q, want %q", tc.name, got, tc.want)
		}
		why := h.Insufficiency(tc.p)
		if (tc.why == "") != (why == "") || !strings.Contains(why, tc.why) {
			t.Errorf("%s: explanation %q, want one containing %q", tc.name, why, tc.why)
		}
	}
	// The all-labels gate is exactly as it was.
	for _, tc := range []struct {
		p    Promotion
		want string
	}{
		{row(29, 29, 12, 0), "insufficient labels (29/30)"},
		{row(30, 29, 12, 0), "insufficient paired labels (29/30)"},
		{row(30, 30, 9, 0), "insufficient disagreements (9/10)"},
		{row(30, 30, 12, 0), "promotable"},
	} {
		if got := tc.p.Verdict(); got != tc.want {
			t.Errorf("all-labels %+v: %q, want %q", tc.p, got, tc.want)
		}
	}
}

// Discordant shortfalls project a split size from the rate seen; none can be
// projected from zero; and the floor never drops below one posting.
func TestHeldOutDiscordantProjectionAndFloorEdges(t *testing.T) {
	h := HeldOut{Split: registered(t), HeldOut: make([]posting.Key, 30)}
	p := Promotion{Asked: 30, Paired: 30, ShadowAnswered: 30, PrimaryAnswered: 30, ShadowOnly: 4}
	if why := h.Insufficiency(p); !strings.Contains(why, "4 discordant, needs 10") || !strings.Contains(why, "a split of about 75 would hold 10") {
		t.Errorf("projection: %q", why)
	}
	p.ShadowOnly = 0
	if why := h.Insufficiency(p); !strings.Contains(why, "0 discordant, needs 10") || strings.Contains(why, "about") {
		t.Errorf("no discordant: %q", why)
	}
	for size, want := range map[int]int{0: 1, 1: 1, 3: 1, 4: 1, 5: 2, 30: 27, 100: 97} {
		if got := (Split{Size: size}).PromotionFloor(); got != want {
			t.Errorf("size %d: floor %d, want %d", size, got, want)
		}
	}
}

// A3-9: a shadow that was asked and answered nothing keeps its row in the
// shadow table, with precision n/a and its unavailable count.
func TestShadowRowWithNoAnswersIsKept(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("gh:co:%d", i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		pre := "triage.work_mode"
		recs = append(recs, mkRec(key, posting.Triaged, map[string]string{
			pre: "remote", pre + ".reading": "answered", pre + ".backend": "d1-ultra", pre + ".p": "0.9",
			pre + ".shadow_reading": "unavailable", pre + ".shadow_backend": "down",
		}))
	}
	// Another shadow answered, so the two rows can be told apart.
	if err := s.Put(Entry{Key: "gh:co:ok", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	pre := "triage.work_mode"
	recs = append(recs, mkRec("gh:co:ok", posting.Triaged, map[string]string{
		pre: "remote", pre + ".reading": "answered", pre + ".backend": "d1-ultra", pre + ".p": "0.9",
		pre + ".shadow": "remote", pre + ".shadow_reading": "answered", pre + ".shadow_backend": "up",
	}))
	rep := BuildReport(recs, s, Names{OpenRouter: "d1-ultra"})
	byName := map[string]ShadowRow{}
	for _, r := range rep.Shadow {
		byName[r.Shadow] = r
	}
	down, ok := byName["down"]
	if !ok || down.Labeled != 4 || down.ShadowAnswered != 0 || down.ShadowUnavailable != 4 {
		t.Fatalf("the shadow that answered nothing: %+v (rows %+v)", down, rep.Shadow)
	}
	if up := byName["up"]; up.ShadowAnswered != 1 || up.ShadowUnavailable != 0 {
		t.Fatalf("the answering shadow: %+v", up)
	}
	md := rep.Markdown()
	if want := "| work_mode | down | 4 | no answers (4 unavailable) | n/a | 0 | n/a (0/0) | 0 | 0 | 0 |"; !strings.Contains(md, want) {
		t.Errorf("markdown lacks %q:\n%s", want, md)
	}
	if want := "| work_mode | up | 1 | 1 | 100.0% (1/1) | 1 |"; !strings.Contains(md, want) {
		t.Errorf("markdown lacks %q:\n%s", want, md)
	}
	if strings.Contains(md, "NaN") {
		t.Errorf("NaN in the report:\n%s", md)
	}
}
