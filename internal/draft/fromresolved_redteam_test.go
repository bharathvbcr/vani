package draft

import (
	"crypto/sha1"
	"encoding/hex"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func rtPass(text string) map[string][]kbsync.Review {
	s := sha1.Sum([]byte(text)) // no tokens or markup in text, so rkb's plain text is the text
	h := hex.EncodeToString(s[:])[:12]
	return map[string][]kbsync.Review{
		"hr": {{Round: 1, Verdict: "pass", TextHash: h, By: "hr-r1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "hr-a"}},
		"hm": {{Round: 1, Verdict: "pass", TextHash: h, By: "hm-r1"}, {Round: 2, Verdict: "pass", TextHash: h, By: "hm-a"}},
	}
}

// cmd/vani/draftcmd.go resolves the WHOLE bank (ResolveBank) and then filters by
// eligibility (FromResolved). ResolveBank marks an entry Blocked when any of its
// bullets, eligible or not, has an unresolved claim token, and FromResolved
// skips a blocked entry. A withdrawn figure is exactly what makes a bullet
// ineligible, so a sibling bullet that is eligible vanishes from every draft
// with no word in the log ("N eligible" still counts it).
func TestRedteamWithdrawnSiblingDoesNotHideAnEligibleBullet(t *testing.T) {
	const good = "Built a plain thing that served real users."
	b := kbsync.Bank{
		Entries: map[string]kbsync.Entry{"proj": {Title: "Engineer", Org: "Acme", Dates: "JAN 2020 – Present", Bullets: []string{"good", "gone"}}},
		Bullets: map[string]kbsync.Bullet{
			"good": {Text: good, Review: rtPass(good)},
			"gone": {Text: "Cut latency by {{clmWithdrawn}}."},
		},
	}
	c := kbsync.Claims{Values: map[string]string{"clmOther": "2x"}, Caution: map[string]bool{}}
	sel := kbsync.Selection{Sections: []kbsync.SelSection{{Kind: "entries", Entries: []kbsync.SelEntry{{Entry: "proj", Bullets: []string{"good", "gone"}}}}}}
	el := kbsync.Eligible(b, c, []kbsync.Selection{sel})
	if !el.Bullets["good"] || el.Bullets["gone"] {
		t.Fatalf("test assumption: good eligible, gone not: %v %v", el.Bullets, el.Why)
	}
	ev := FromResolved(kbsync.ResolveBank(b, c), el)
	if len(ev) != 1 || len(ev[0].Bullets) != 1 {
		t.Fatalf("FAIL: the eligible bullet is missing from draft evidence (%d entries): ResolveBank blocked the whole entry over the withdrawn sibling", len(ev))
	}
}
