package rkbresume

import (
	"context"
	"strings"
	"testing"
)

// offered parses the evidence section of a prompt into entry id ("" for the
// summary) -> bullet ids, exactly as the model reads it.
func offered(prompt string) map[string][]string {
	out := map[string][]string{}
	cur := "?"
	for _, l := range strings.Split(prompt, "\n") {
		switch {
		case l == "Summary evidence:":
			cur = ""
		case strings.HasPrefix(l, "Entry ") && strings.HasSuffix(l, "):"):
			cur = strings.Fields(strings.TrimPrefix(l, "Entry "))[0]
		case strings.HasPrefix(l, "  ") && cur != "?":
			id := strings.TrimSpace(l)
			if i := strings.IndexAny(id, ": "); i > 0 {
				id = id[:i]
			}
			out[cur] = append(out[cur], id)
		case l == "" || strings.HasPrefix(l, "("):
			cur = "?"
		}
	}
	return out
}

// Whatever the prompt offers, in either turn, kbsync's own Eligibility allows
// under that entry; and no ineligible bullet's text appears anywhere in it.
func TestRedteamEveryOfferedBulletIsEligibleUnderItsEntry(t *testing.T) {
	f := newFixture(t)
	// b1 is in the base and loses a reader pass: ineligible.
	editJSON(t, f.bank, func(m map[string]any) {
		delete(m["bullets"].(map[string]any)["b1"].(map[string]any), "review")
	})
	f.rkb.lint = func(string) (int, string) { return 1, "ERROR a1: forces a retry\n" }
	f.provider.replies = []string{validReply()}
	_, _ = f.build() // fails closed at the base; the prompts are what is under test
	if len(f.provider.reqs) < 2 {
		t.Fatalf("want a first and a retry turn, got %d", len(f.provider.reqs))
	}

	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.el.Allowed("exp-b", "b1") {
		t.Fatal("scenario: b1 is still eligible")
	}
	for i, r := range f.provider.reqs {
		text := userText(r)
		for entry, ids := range offered(text) {
			for _, id := range ids {
				if !b.el.Allowed(entry, id) {
					t.Errorf("turn %d offers %q under %q, which kbsync.Eligibility does not allow", i, id, entry)
				}
			}
		}
		for _, banned := range []string{bulletTexts["b1"], bulletTexts["x1"]} {
			if strings.Contains(text, banned) {
				t.Errorf("turn %d carries the text of an ineligible bullet", i)
			}
		}
	}
	// And the property is not vacuous: something is offered.
	if n := len(offered(userText(f.provider.reqs[0]))["exp-a"]); n == 0 {
		t.Errorf("parser found no bullets offered under exp-a")
	}
}
