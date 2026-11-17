package digest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func w4aEvicted(key string) ledger.Record {
	d := dropped(key, "title", "role_fit", "mismatch_domain")
	d.BodyEvicted = true
	d.Posting.Description = ""
	return d
}

// kbSeen records the description of every record the ranking asks the
// knowledge-base match about.
type w4aSeen struct{ texts map[posting.Key][]string }

func (s *w4aSeen) kb(r ledger.Record) (jobinsight.Slot, bool) {
	s.texts[r.Key] = append(s.texts[r.Key], r.Posting.Description)
	return jobinsight.Slot{}, false
}

// A pick whose text reads back is scored on that text.
func TestW4aExploreHydratesEvictedPick(t *testing.T) {
	s := &w4aSeen{texts: map[posting.Key][]string{}}
	var asked []posting.Key
	o := Options{Rules: rules, ExploreDropped: 1, KBMatch: s.kb,
		Hydrate: func(r ledger.Record) (ledger.Record, bool) {
			asked = append(asked, r.Key)
			r.Posting.Description, r.BodyEvicted = "real text", false
			return r, true
		}}
	md, _, shown := RenderShown([]ledger.Record{w4aEvicted("gh:d:one")}, nil, day, Counts{}, o)
	if len(asked) != 1 || asked[0] != "gh:d:one" {
		t.Fatalf("hydrate asked for %v", asked)
	}
	if got := s.texts["gh:d:one"]; len(got) == 0 || slicesContain(got, "") {
		t.Fatalf("scored on %q, want only the real text", got)
	}
	if strings.Contains(md, "not scored") || len(shown) != 1 || shown[0].NotScored {
		t.Fatalf("a hydrated pick was shown unscored:\n%s", md)
	}
	if !strings.Contains(md, "Relevance") {
		t.Fatalf("no score on the row:\n%s", md)
	}
}

// A pick whose text cannot be read back is listed unscored, never scored.
func TestW4aExploreUnreadablePickIsNotScored(t *testing.T) {
	for name, hook := range map[string]func(ledger.Record) (ledger.Record, bool){
		"nil hook":      nil,
		"read failed":   func(r ledger.Record) (ledger.Record, bool) { return r, false },
		"still evicted": func(r ledger.Record) (ledger.Record, bool) { return r, true },
	} {
		t.Run(name, func(t *testing.T) {
			s := &w4aSeen{texts: map[posting.Key][]string{}}
			o := Options{Rules: rules, ExploreDropped: 1, KBMatch: s.kb, Hydrate: hook}
			md, _, shown := RenderShown([]ledger.Record{w4aEvicted("gh:d:gone")}, nil, day, Counts{}, o)
			if len(s.texts) != 0 {
				t.Fatalf("an unreadable pick was scored: KBMatch saw %v", s.texts)
			}
			if !strings.Contains(md, "not scored: text not loaded") || strings.Contains(md, "Relevance") {
				t.Fatalf("digest:\n%s", md)
			}
			if len(shown) != 1 || !shown[0].NotScored || shown[0].Source != ExploreSourceDropped || shown[0].PSelect <= 0 {
				t.Fatalf("shown %+v", shown)
			}
		})
	}
}

// The reads are bounded by the Explore limits however many records are evicted,
// and a resident pick is never hydrated.
func TestW4aExploreHydrationIsBounded(t *testing.T) {
	var recs []ledger.Record
	for i := 0; i < 60; i++ {
		recs = append(recs, w4aEvicted(fmt.Sprintf("gh:d:%02d", i)))
	}
	recs = append(recs, dropped("gh:d:resident", "title", "role_fit", "mismatch_domain"))
	calls := 0
	o := Options{Rules: rules, ExploreDropped: 1000, Hydrate: func(r ledger.Record) (ledger.Record, bool) {
		calls++
		if !r.BodyEvicted {
			t.Errorf("hydrate asked about the resident %s", r.Key)
		}
		r.Posting.Description, r.BodyEvicted = "text", false
		return r, true
	}}
	_, _, shown := RenderShown(recs, nil, day, Counts{}, o)
	if len(shown) != MaxExplore || calls > MaxExplore {
		t.Fatalf("%d rows shown, %d hydrations; each at most %d", len(shown), calls, MaxExplore)
	}
}

// A saved closed role whose body could not be read back is listed under Saved
// without a recorded score; a resident one keeps its score. (The order within
// Saved still sorts on the ranking, which has no text for the evicted one.)
func TestW4aSavedEvictedRowIsNotScored(t *testing.T) {
	lost := w4aEvicted("gh:d:lost")
	kept := dropped("gh:d:kept", "title", "role_fit", "mismatch_domain")
	m := map[posting.Key]marks.Mark{"gh:d:lost": marks.Saved, "gh:d:kept": marks.Saved}
	_, _, shown := RenderShown([]ledger.Record{lost, kept}, m, day, Counts{}, Options{Rules: rules})
	got := map[posting.Key]Shown{}
	for _, s := range shown {
		if s.Section == SectionSaved {
			got[s.Key] = s
		}
	}
	if len(got) != 2 || !got["gh:d:lost"].NotScored || got["gh:d:kept"].NotScored {
		t.Fatalf("saved rows: %+v", got)
	}
}

func slicesContain(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
