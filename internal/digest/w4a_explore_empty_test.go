package digest

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// An Explore pick whose body is evicted from memory is never scored on its
// empty description: whatever the digest hands the ranking's text-keyed
// readers (here the knowledge-base match) for it is real text or nothing.
// This test uses no hydrate hook, so it also runs against the code before the
// hook existed: there the evicted record reached KBMatch with "".
func TestW4aExploreNeverScoresEvictedEmptyText(t *testing.T) {
	d := dropped("gh:d:evicted", "title", "role_fit", "mismatch_domain")
	d.BodyEvicted = true
	d.Posting.Description = ""
	var seen []string
	o := Options{Rules: rules, ExploreDropped: 1, KBMatch: func(r ledger.Record) (jobinsight.Slot, bool) {
		seen = append(seen, r.Posting.Description)
		return jobinsight.Slot{}, false
	}}
	_, _, shown := RenderShown([]ledger.Record{d}, nil, day, Counts{}, o)
	if len(sectionKeys(shown, SectionExplore)) != 1 {
		t.Fatalf("the evicted dropped role was not explored: %+v", shown)
	}
	for _, text := range seen {
		if text == "" {
			t.Fatalf("the evicted record was scored on an empty description (KBMatch saw %q)", seen)
		}
	}
}
