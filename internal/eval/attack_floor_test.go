package eval

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var advisedSize = regexp.MustCompile(`a split of size (-?\d+) or less`)
var negNum = regexp.MustCompile(`(^|[ (])-\d`)

// Attack 5: across a grid of counts, a verdict that says "insufficient ..." and
// an Insufficiency explanation agree, nothing is negative, and the advised
// split size really would clear the floor (Split{Size: s}.PromotionFloor() <=
// the count advised on).
func TestAttackHeldOutFloorExplanationsAreConsistent(t *testing.T) {
	for _, size := range []int{1, 2, 3, 4, 30, 31} {
		h := HeldOut{Split: Split{Name: "x", Size: size}, HeldOut: make([]posting.Key, size)}
		for asked := 0; asked <= size+2; asked++ {
			for prim := 0; prim <= asked; prim += max(1, asked/3) {
				for sh := 0; sh <= asked; sh += max(1, asked/3) {
					for paired := 0; paired <= min(prim, sh); paired += max(1, asked/3) {
						for so := 0; so <= paired; so += max(1, paired/2) {
							p := Promotion{Slot: "work_mode", Shadow: "s", Primary: "p", Asked: asked,
								PrimaryAnswered: prim, ShadowAnswered: sh, Paired: paired, ShadowOnly: so, PrimaryOnly: (paired - so) / 2}
							why := h.Insufficiency(p)
							v := h.Verdict(p)
							insufficientVerdict := strings.HasPrefix(v, "insufficient")
							if why != "" && !insufficientVerdict {
								// An explanation for a row whose verdict is not insufficient.
								t.Errorf("size %d %+v: verdict %q but explanation %q", size, p, v, why)
							}
							if insufficientVerdict && why == "" && p.PrimaryAnswered > 0 {
								t.Errorf("size %d %+v: verdict %q has no explanation", size, p, v)
							}
							if negNum.MatchString(why) {
								t.Errorf("size %d %+v: negative number in %q", size, p, why)
							}
							for _, m := range advisedSize.FindAllStringSubmatch(why, -1) {
								s, _ := strconv.Atoi(m[1])
								count := p.Asked
								if strings.Contains(why, "paired, needs") {
									count = p.Paired
								}
								if strings.Contains(why, "asked, needs") {
									count = p.Asked
								}
								if count == 0 {
									continue // the zero case is in attack_fail_test.go
								}
								if s < 1 || (Split{Size: s}).PromotionFloor() > count {
									t.Errorf("size %d %+v: advises split size %d whose floor %d exceeds the %d it rests on: %q",
										size, p, s, (Split{Size: s}).PromotionFloor(), count, why)
								}
							}
						}
					}
				}
			}
		}
	}
}

// Attack 5: boundaries 26/27/28 and 29/30 on asked and paired, 9/10 discordant.
func TestAttackHeldOutFloorBoundaries(t *testing.T) {
	h := HeldOut{Split: Split{Name: "x", Size: 30}, HeldOut: make([]posting.Key, 30)}
	mk := func(asked, paired, disc int) Promotion {
		return Promotion{Asked: asked, PrimaryAnswered: 20, ShadowAnswered: 30 - (30 - asked), Paired: paired, ShadowOnly: disc}
	}
	for _, c := range []struct {
		asked, paired, disc int
		want                string
	}{
		{26, 26, 12, "insufficient labels (26/27)"},
		{27, 27, 12, "promotable"},
		{28, 28, 12, "promotable"},
		{30, 30, 12, "promotable"},
		{29, 29, 9, "insufficient disagreements (9/10)"},
		{29, 29, 10, "promotable"},
		{30, 26, 12, "insufficient paired labels (26/27)"},
		{30, 27, 12, "promotable"},
	} {
		p := mk(c.asked, c.paired, c.disc)
		if p.ShadowAnswered < p.Paired {
			p.ShadowAnswered = p.Paired
		}
		p.PrimaryAnswered = p.Paired
		if got := h.Verdict(p); got != c.want {
			t.Errorf("%+v: %q, want %q", c, got, c.want)
		}
	}
	// Split size 30 with 4 skips: asked 26.
	if (Split{Size: 30}).PromotionFloor() != 27 || (Split{Size: 2}).PromotionFloor() != 1 || (Split{Size: 0}).PromotionFloor() != 1 || (Split{Size: -5}).PromotionFloor() != 1 {
		t.Error("PromotionFloor")
	}
}

// Attack 6: a shadow asked about nothing at all (Labeled 0) never gets a row, a
// shadow asked and unavailable everywhere shows "no answers (N unavailable)",
// and the caption claim holds for both.
func TestAttackShadowRowZeroAnsweredZeroUnavailable(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("gh:co:%02d", i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		pre := "triage.work_mode"
		ann := map[string]string{pre: "remote", pre + ".reading": "answered", pre + ".backend": "d1-ultra"}
		switch {
		case i < 2: // shadow recorded as unavailable
			ann[pre+".shadow_reading"] = "unavailable"
			ann[pre+".shadow_backend"] = "local"
		case i < 4: // shadow named but no reading recorded at all (legacy)
			ann[pre+".shadow_backend"] = "legacy-down"
		}
		recs = append(recs, mkRec(key, posting.Triaged, ann))
	}
	all, _, err := BuildHeldOut(recs, s, Names{OpenRouter: "d1-ultra"}, registered(t))
	if err != nil {
		t.Fatal(err)
	}
	md := all.Markdown()
	for _, sr := range all.Shadow {
		t.Logf("shadow row %+v", sr)
	}
	if !strings.Contains(md, "| work_mode | local | 2 | no answers (2 unavailable) | n/a |") {
		t.Errorf("unavailable shadow row missing:\n%s", md)
	}
	if strings.Contains(md, "legacy-down") {
		t.Errorf("a shadow with no recorded reading got a row")
	}
	if strings.Contains(md, "NaN") || strings.Contains(md, "Inf") {
		t.Errorf("non-finite in report")
	}
}

// Attack 1: the report bytes do not depend on map iteration order (several
// shadows, slots and primaries, 50 builds).
func TestAttackReportBytesDeterministic(t *testing.T) {
	s, _, _ := newStore(t)
	var recs []ledger.Record
	for i := 0; i < 12; i++ {
		key := fmt.Sprintf("gh:co:%02d", i)
		if err := s.Put(Entry{Key: posting.Key(key), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
		pre := "triage.work_mode"
		ann := map[string]string{pre: "remote", pre + ".reading": "answered", pre + ".backend": "d1-ultra", pre + ".shadow_backend": fmt.Sprintf("local%d", i%4)}
		if i%3 == 0 {
			ann[pre+".shadow_reading"] = "unavailable"
		} else {
			ann[pre+".shadow"], ann[pre+".shadow_reading"] = "remote", "answered"
		}
		recs = append(recs, mkRec(key, posting.Triaged, ann))
	}
	var first string
	for run := 0; run < 50; run++ {
		all, h, err := BuildHeldOut(recs, s, Names{OpenRouter: "d1-ultra"}, registered(t))
		if err != nil {
			t.Fatal(err)
		}
		got := all.Markdown() + h.Markdown()
		if run == 0 {
			first = got
		} else if got != first {
			t.Fatalf("run %d differs", run)
		}
	}
}
