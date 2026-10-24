package rkbresume

import (
	"fmt"
	"regexp"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Some bank bullets read as the continuation of the bullet before them:
// "Maintain the ingestion layers beneath those features", "Brought the same
// lessons to NVIDIA". Alone on a page they refer to nothing. A bullet is a
// continuation when its text holds a demonstrative back-reference, and it may
// be shown only directly after the bullet it followed in the owner's own
// selections.
//
// The patterns are narrow on purpose: a missed continuation is a dangling
// reference on the page, a false match only keeps one bullet away from the
// model. continuation_test.go runs them over the live bank when it is present
// and pins the list they match.
var continuationRes = []*regexp.Regexp{
	// "the systems beneath those features"
	regexp.MustCompile(`(?i)\b(?:beneath|behind|underneath|under|beyond|atop) (?:those|these) [a-z][a-z-]*`),
	// "pipelines that feed those models"
	regexp.MustCompile(`(?i)\b(?:feeds?|fed|serves?|powers?|consumes?|runs on) (?:those|these) [a-z][a-z-]*`),
	// "Brought the same lessons to NVIDIA"
	regexp.MustCompile(`(?i)\bthe same (?:lessons?|work|approach|methods?|techniques?|ideas?|patterns?|principles?|practices?) (?:to|on|for|in|at|across)\b`),
	// "{{clm}} of that from phase-edge synchronization"
	regexp.MustCompile(`(?i)^[\s*]*(?:\{\{clm[A-Za-z]+\}\}|[0-9][0-9.,]*[%×x]?) of that\b`),
	// "Confirmed it with 2 independent methods"
	regexp.MustCompile(`^[\s*]*[A-Z][a-z]+ed it with\b`),
	// "Also trains and evaluates models": a bullet that opens with "also" has an
	// elided subject and adds to what came before it. Alone, or first in a
	// summary, it adds to nothing. The owner keeps it directly after the lead
	// bullet it extends, and only in that order is it offered.
	regexp.MustCompile(`(?i)^[\s*]*also\b`),
}

// isContinuation reports whether a bullet's text refers back to a bullet
// before it.
func isContinuation(text string) bool {
	for _, re := range continuationRes {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// continuations says, for each bullet that is a continuation, the bullet it
// must directly follow within an entry (entry "" is a summary section). An
// empty predecessor means the bullet came first in its list: it has nothing to
// follow and is never offered.
type continuations map[string]map[string]string

// buildContinuations reads the order the owner wrote. The predecessor of a
// continuation is the bullet directly before it under the same entry in the
// first selection (in the order given) that lists it there. text yields a
// bullet's raw text, and false for an id the bank lacks.
func buildContinuations(sels []kbsync.Selection, text func(id string) (string, bool)) continuations {
	out := continuations{}
	note := func(entry string, ids []string) {
		for i, id := range ids {
			if _, seen := out[entry][id]; seen {
				continue
			}
			t, ok := text(id)
			if !ok || !isContinuation(t) {
				continue
			}
			if out[entry] == nil {
				out[entry] = map[string]string{}
			}
			pred := ""
			if i > 0 {
				pred = ids[i-1]
			}
			out[entry][id] = pred
		}
	}
	for _, s := range sels {
		for _, sec := range s.Sections {
			switch sec.Kind {
			case "bullets":
				note("", sec.Bullets)
			case "entries":
				for _, e := range sec.Entries {
					note(e.Entry, e.Bullets)
				}
			}
		}
	}
	return out
}

// is reports whether id is a continuation under entry.
func (c continuations) is(entry, id string) bool {
	_, ok := c[entry][id]
	return ok
}

// pred is the bullet a continuation must directly follow; "" when it has none.
func (c continuations) pred(entry, id string) string { return c[entry][id] }

// check refuses a list of bullets, in page order under one entry, in which a
// continuation is not directly after its predecessor.
func (c continuations) check(entry string, ids []string) error {
	for i, id := range ids {
		req, ok := c[entry][id]
		if !ok {
			continue
		}
		where := "its entry " + entry
		if entry == "" {
			where = "its section"
		}
		if req == "" {
			return fmt.Errorf("bullet %q continues an earlier bullet that is not available, so it cannot be used", clip(id, 60))
		}
		if i == 0 || ids[i-1] != req {
			return fmt.Errorf("bullet %q continues the bullet before it and must come directly after %q in %s", clip(id, 60), req, where)
		}
	}
	return nil
}
