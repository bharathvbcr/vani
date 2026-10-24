package semantic

import (
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

func eligibility(entryBullets map[string][]string) kbsync.Eligibility {
	el := kbsync.Eligibility{Bullets: map[string]bool{}, EntryBullets: entryBullets}
	for _, ids := range entryBullets {
		for _, id := range ids {
			el.Bullets[id] = true
		}
	}
	return el
}

// Eligibility.Restrict keeps a bullet only under the entry a selection lists it for,
// drops an entry left with nothing (its title and stack with it), keeps the
// bank's order, and leaves the bank it was given alone.
func TestEligibleBankCutsToWhatIsAllowed(t *testing.T) {
	b, err := kbsync.Parse([]byte(synthBank))
	if err != nil {
		t.Fatal(err)
	}
	// b3 is allowed under e2; b1 only under a different entry; e1 keeps b2 alone.
	cut := eligibility(map[string][]string{"e1": {"b2"}, "e2": {"b4", "b3"}, "ghost": {"b1"}}).Restrict(b)
	if got := strings.Join(cut.Entries["e1"].Bullets, ","); got != "b2" {
		t.Errorf("e1 bullets = %q, want b2", got)
	}
	if got := strings.Join(cut.Entries["e2"].Bullets, ","); got != "b3,b4" {
		t.Errorf("e2 bullets = %q, want the bank's order b3,b4", got)
	}
	if _, ok := cut.Bullets["b1"]; ok {
		t.Error("b1 is allowed under no entry the bank has, yet it was kept")
	}
	if len(b.Entries["e1"].Bullets) != 2 || len(b.Bullets) != 4 {
		t.Error("the bank passed in was modified")
	}

	p, err := ProfileFromBank(cut, target)
	if err != nil {
		t.Fatal(err)
	}
	all := synthProfile(t)
	if len(p.Chunks) >= len(all.Chunks) || p.Hash() == all.Hash() {
		t.Errorf("cut profile has %d chunks (whole %d); hash equal: %v", len(p.Chunks), len(all.Chunks), p.Hash() == all.Hash())
	}
	for _, c := range p.Chunks {
		if strings.Contains(c.Text, "Kubernetes clusters") {
			t.Errorf("b1 reached the profile: %q", c.Text)
		}
	}

	// An entry with no allowed bullet disappears whole.
	only := eligibility(map[string][]string{"e2": {"b4"}}).Restrict(b)
	if _, ok := only.Entries["e1"]; ok || len(only.Entries) != 1 {
		t.Errorf("entries = %v", only.Entries)
	}
	pr, err := ProfileFromBank(only, target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(chunks(pr), "Platform Engineer") || strings.Contains(chunks(pr), "Go, Kubernetes") {
		t.Errorf("a role with no eligible bullet left its title or stack in the profile: %q", chunks(pr))
	}
}

func chunks(p Profile) string {
	var b strings.Builder
	for _, c := range p.Chunks {
		b.WriteString(c.Label + " | " + c.Text + "\n")
	}
	return b.String()
}

// Nothing allowed is an empty bank, and an empty bank is an error, not a
// profile of nothing: matching reads "not run".
func TestEligibleBankNothingAllowedIsEmpty(t *testing.T) {
	b, err := kbsync.Parse([]byte(synthBank))
	if err != nil {
		t.Fatal(err)
	}
	for name, el := range map[string]kbsync.Eligibility{
		"zero value":        {},
		"bullet no entry":   {Bullets: map[string]bool{"b1": true}},
		"summary bullet":    {Bullets: map[string]bool{"b1": true}, Summary: []string{"b1"}},
		"wrong entry":       eligibility(map[string][]string{"e2": {"b1"}}),
		"missing bullet id": eligibility(map[string][]string{"e1": {"nope"}}),
	} {
		cut := el.Restrict(b)
		if len(cut.Entries) != 0 || len(cut.Bullets) != 0 {
			t.Errorf("%s: kept %v %v", name, cut.Entries, cut.Bullets)
		}
		if _, err := ProfileFromBank(cut, target); !errors.Is(err, ErrEmpty) {
			t.Errorf("%s: err = %v, want ErrEmpty", name, err)
		}
	}
}
