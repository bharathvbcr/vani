package eval

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func droppedWith(ann map[string]string) ledger.Record {
	return ledger.Record{Key: "gh:a:1", Status: posting.Dropped, Annotations: ann}
}

var classRules = []DropRule{
	{Slot: "role_fit", Values: []string{"mismatch_domain"}},
	{Slot: "seniority", Values: []string{"staff_plus", "manager"}},
}

// An exclusion is an engine rule on the posting's text, not a model reading:
// its drops are their own class, named by the field that matched and never by
// the matched value, whichever path dropped the posting.
func TestDropClassNamesExclusionsByField(t *testing.T) {
	cases := []struct {
		name string
		ann  map[string]string
		want string
	}{
		// Dropped at triage: the exclusion answered before any backend.
		{"triage title", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: `title matches "sales"`}, "exclude:title"},
		{"triage company", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: `company is "Initech"`}, "exclude:company"},
		{"triage location", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: `location matches "London"`}, "exclude:location"},
		{"triage only_locations", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: "location is outside only_locations"}, "exclude:location"},
		{"triage description", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: "description matches an excluded phrase"}, "exclude:description"},
		// Dropped later by ApplyExclusions: the model's earlier reading stays
		// on the record, and must not take the credit (or the blame).
		{"applied over a model drop reading", map[string]string{
			"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", "triage.role_fit.backend": "d1-ultra",
			AnnExcludeMatched: "excluded: " + `title matches "sales"`,
		}, "exclude:title"},
		{"applied over a title reading", map[string]string{
			"triage.backend": "title", "triage.seniority": "manager", AnnExcludeMatched: `company is "Initech"`,
		}, "exclude:company"},
		// A reason this code does not know is still an exclusion.
		{"unknown reason", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: "something new"}, "exclude:other"},
		{"exclude backend, reason lost", map[string]string{"triage.backend": "exclude"}, "exclude:other"},
		// The pre-existing classes are unchanged.
		{"model", map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain"}, "model:role_fit"},
		{"title", map[string]string{"triage.backend": "title", "triage.seniority": "staff_plus"}, "title:seniority"},
		{"no backend", map[string]string{"triage.role_fit": "mismatch_domain"}, "unknown:role_fit"},
		{"lappi", map[string]string{"triage.backend": "lappi", "triage.seniority": "manager"}, "model:seniority"},
	}
	for _, c := range cases {
		if got := DropClass(droppedWith(c.ann), classRules); got != c.want {
			t.Errorf("%s: DropClass = %q, want %q", c.name, got, c.want)
		}
		if src := "dropped:" + DropClass(droppedWith(c.ann), classRules); !validSource(src) {
			t.Errorf("%s: %q is not a valid label source", c.name, src)
		}
	}
	// A cleared exclusion (reopened, then dropped by a model) is the model's.
	r := droppedWith(map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", AnnExcludeMatched: ""})
	if got := DropClass(r, classRules); got != "model:role_fit" {
		t.Errorf("cleared exclusion: %q", got)
	}
	// Not dropped: no class, exclusion annotation or not.
	r.Status = posting.Digested
	r.Annotations[AnnExcludeMatched] = "title matches \"x\""
	if got := DropClass(r, classRules); got != "" {
		t.Errorf("kept record classed %q", got)
	}
}

// LegacyDropCell names the cell an exclusion drop had before exclusions were
// a class of their own, so labels drawn under that name still count; a record
// whose class did not change has none.
func TestLegacyDropCellNamesTheCellBeforeTheRename(t *testing.T) {
	cases := []struct {
		name string
		ann  map[string]string
		want string
	}{
		{"exclusion at triage", map[string]string{"triage.backend": "exclude", AnnExcludeMatched: `company is "Initech"`}, "dropped:model:other"},
		{"exclusion over a model reading", map[string]string{
			"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", "triage.role_fit.backend": "d1-ultra",
			AnnExcludeMatched: "excluded: " + `title matches "sales"`,
		}, "dropped:model:role_fit"},
		{"model drop", map[string]string{"triage.backend": "d1-ultra", "triage.role_fit": "mismatch_domain", "triage.role_fit.backend": "d1-ultra"}, ""},
		{"title drop", map[string]string{"triage.backend": "title", "triage.seniority": "manager"}, ""},
	}
	for _, c := range cases {
		if got := LegacyDropCell(droppedWith(c.ann), classRules); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := LegacyDropCell(ledger.Record{Key: "gh:a:2", Status: posting.Digested}, classRules); got != "" {
		t.Errorf("a kept record: %q", got)
	}
}
