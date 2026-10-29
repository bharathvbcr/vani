package jobview

import (
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Scope.Keep is the rule a scoped run advances by; Select is the rule `jobs
// list` lists by. Over every posting and scope below they agree, so the
// pending view with the same flags is an exact preview of the run.
func TestScopeKeepAgreesWithSelect(t *testing.T) {
	postings := []posting.Posting{
		{Company: "Acme", Location: "Austin, TX"},
		{Company: "  ACME\t", Location: "Dallas, Texas"},
		{Company: "Acme", Location: "San Francisco, CA; Remote"},
		{Company: "Acme Corp", Location: "El Paso, TX"},
		{Company: "Initech", Location: "Remote", Remote: true},
		{Company: "Initech", Location: ""},
		{Company: "", Location: "Austin, TX"},
		{Company: "Globex", Location: "London, United Kingdom"},
		{Company: "Globex", Location: "Remote - US"},
		{Company: "Acme​", Location: "Austin, TX"},
		{Company: "Acme\x07", Location: "Austin, TX"}, // a control rune jobs list cleans out
	}
	set := func(xs ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	scopes := []Scope{
		{},
		{Companies: set("acme")},
		{Companies: set("acme", "initech")},
		{Companies: set("acme corp")},
		{States: set("TX")},
		{States: set("none")},
		{Areas: set("el paso")},
		{Areas: set("none")},
		{Remote: true},
		{States: set("CA"), OrRemote: true},
		{Companies: set("acme"), States: set("TX"), Remote: true},
		{Companies: set("globex"), Areas: set("london")},
	}
	kept, left := 0, 0
	for si, s := range scopes {
		o := Opts{View: "all", States: s.States, Areas: s.Areas, Remote: s.Remote, OrRemote: s.OrRemote, More: Filter{Companies: s.Companies}}
		for pi, p := range postings {
			key := posting.Key(fmt.Sprintf("gh:x:%d", pi))
			p.Key = key
			r := ledger.Record{Key: key, Status: posting.Triaged, Posting: p}
			sel, _ := Select(ledger.Snapshot{Records: []ledger.Record{r}}, map[posting.Key]marks.Entry{}, &o, noRank{}, 0)
			listed := len(sel) == 1
			if got := s.Keep(p); listed != got {
				t.Errorf("scope %d %+v, posting %q at %q: jobs list %v, Keep %v", si, s, p.Company, p.Location, listed, got)
			}
			if listed {
				kept++
			} else {
				left++
			}
		}
	}
	if kept < 20 || left < 20 {
		t.Errorf("the grid tells little: %d kept, %d left out", kept, left)
	}
}

// noRank enriches nothing: the scope reads no score.
type noRank struct{}

func (noRank) Enrich(*Job, ledger.Record, int) []jobinsight.Hit { return nil }

func TestScopeIsZero(t *testing.T) {
	if !(Scope{}).IsZero() {
		t.Error("an empty scope is not zero")
	}
	for _, s := range []Scope{{Companies: map[string]bool{}}, {States: map[string]bool{"TX": true}}, {Remote: true}, {OrRemote: true}} {
		if s.IsZero() {
			t.Errorf("%+v reads as zero", s)
		}
	}
}
