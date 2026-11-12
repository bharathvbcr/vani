package eval

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func closedPool(nKept, nDropped, nDead, nDup int) []AuditCandidate {
	var cs []AuditCandidate
	for i := range nKept {
		cs = append(cs, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:k:%03d", i)), Score: float64(i)})
	}
	for i := range nDropped {
		cs = append(cs, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:d:%03d", i)), Dropped: true, Class: "model:role_fit"})
	}
	for i := range nDead {
		cs = append(cs, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:x:%03d", i)), Closed: true, Class: "dead"})
	}
	for i := range nDup {
		cs = append(cs, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:u:%03d", i)), Closed: true, Class: "duplicate"})
	}
	return cs
}

// Adding the closed group must not move a single scored or dropped pick, or
// its p_select: each group spends only its own places.
func TestAuditDrawClosedLeavesTheOtherGroupsAsTheyWere(t *testing.T) {
	cands := closedPool(40, 25, 10, 3)
	for _, seed := range []string{"2026-W46", "a", "b", "c"} {
		old := AuditSample(cands, nil, seed, AuditScored, AuditDropped)
		got := AuditDraw(cands, nil, seed, DefaultAuditQuotas)
		var rest []AuditPick
		var closed []AuditPick
		for _, p := range got.Picks {
			if strings.HasPrefix(p.Source, ClosedPrefix) {
				closed = append(closed, p)
			} else {
				rest = append(rest, p)
			}
		}
		if !reflect.DeepEqual(rest, old.Picks) {
			t.Fatalf("seed %s: the scored and dropped picks moved:\n%v\n%v", seed, rest, old.Picks)
		}
		if len(closed) != AuditClosed {
			t.Fatalf("seed %s: %d closed picks, want %d", seed, len(closed), AuditClosed)
		}
		for _, p := range closed {
			want := map[string]float64{"closed:dead": 1.0 / 10, "closed:duplicate": 1.0 / 3}[p.Source]
			if want == 0 || p.PSelect != want {
				t.Errorf("seed %s: %s p_select %v, want %v", seed, p.Source, p.PSelect, want)
			}
		}
	}
	// With no closed quota the plan is AuditSample's, strata included.
	if a, b := AuditDraw(cands, nil, "w", AuditQuotas{Scored: 5, Dropped: 10}), AuditSample(cands, nil, "w", 5, 10); !reflect.DeepEqual(a, b) {
		t.Errorf("Closed 0 differs from AuditSample:\n%+v\n%+v", a, b)
	}
}

func TestAuditDrawClosedEdges(t *testing.T) {
	// One closed place over two strata: each is served half the time.
	cands := closedPool(0, 0, 4, 4)
	plan := AuditDraw(cands, nil, "w", AuditQuotas{Closed: 1})
	if len(plan.Picks) != 1 || plan.Picks[0].PSelect != 0.5/4 {
		t.Errorf("partial round: %+v", plan.Picks)
	}
	// A labelled closed posting is never drawn; a pool smaller than its places
	// is drawn whole with p_select 1, not padded.
	labeled := map[posting.Key]Entry{"gh:x:000": {}, "gh:x:001": {}, "gh:x:002": {}}
	plan = AuditDraw(closedPool(0, 0, 4, 0), labeled, "w", AuditQuotas{Closed: 5})
	if len(plan.Picks) != 1 || plan.Picks[0].Key != "gh:x:003" || plan.Picks[0].PSelect != 1 {
		t.Errorf("labelled or short pool: %+v", plan.Picks)
	}
	// An odd status still makes a valid label source.
	odd := []AuditCandidate{{Key: "gh:z:1", Closed: true, Class: "Weird Status!"}, {Key: "gh:z:2", Closed: true}}
	plan = AuditDraw(odd, nil, "w", AuditQuotas{Closed: 2})
	for _, p := range plan.Picks {
		if !validSource(p.Source) || !strings.HasPrefix(p.Source, ClosedPrefix) {
			t.Errorf("source %q", p.Source)
		}
	}
	// A closed candidate is never scored or dropped, even with Dropped set.
	both := []AuditCandidate{{Key: "gh:z:3", Closed: true, Dropped: true, Class: "dead"}}
	if plan = AuditDraw(both, nil, "w", AuditQuotas{Scored: 5, Dropped: 5}); len(plan.Picks) != 0 {
		t.Errorf("closed candidate drawn by another group: %+v", plan.Picks)
	}
	if got := AuditCell(both[0]); got != "closed:dead" {
		t.Errorf("AuditCell = %q", got)
	}
	if got := AuditCell(AuditCandidate{Dropped: true}); got != "dropped:unknown:other" {
		t.Errorf("AuditCell of a classless drop = %q", got)
	}
	if got := AuditCell(AuditCandidate{Score: 3}); got != CellKept {
		t.Errorf("AuditCell of a kept posting = %q", got)
	}
}
