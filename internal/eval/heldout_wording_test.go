package eval

import (
	"strings"
	"testing"
)

// The held-out documentation says which keys the split ranks, and the code
// agrees: a key with a triage-slot label is ranked (an all-skip one too), a key
// whose line holds only would_apply is not, and the rendered section names the
// triage filter instead of "every distinct key in the label file".
func TestHeldOutWordingMatchesTheFilterTheCodeApplies(t *testing.T) {
	s, _, _ := newStore(t)
	for _, e := range []Entry{
		{Key: "gh:co:triage", Labels: map[string]string{"work_mode": "remote"}},
		{Key: "gh:co:skip", Labels: map[string]string{"work_mode": Skip}},
		{Key: "gh:co:audit", Labels: map[string]string{WouldApply: "yes"}, Source: SourceManual},
		{Key: "gh:co:audit2", Labels: map[string]string{WouldApply: "no"}, Source: SourceManual},
	} {
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	_, h, err := BuildHeldOut(nil, s, Names{}, Split{Name: "w", Salt: "salt", Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	if h.Ranked != 2 || len(h.HeldOut) != 2 {
		t.Fatalf("ranked %d held %d, want the 2 triage-labelled keys: %v", h.Ranked, len(h.HeldOut), h.HeldOut)
	}
	md := h.Markdown()
	if strings.Contains(md, "every distinct key in the label file") {
		t.Errorf("the section still says every key is ranked:\n%s", md)
	}
	if !strings.Contains(md, "triage slot") || !strings.Contains(md, "would_apply-only") {
		t.Errorf("the section does not name the triage filter:\n%s", md)
	}
}
