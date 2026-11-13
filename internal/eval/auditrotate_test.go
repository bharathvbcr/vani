package eval

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Picks are labelled week by week, as the owner answers them: classes empty
// out and the set of strata changes. Every class is still reached, within
// twice the no-label bound.
func TestAuditRotationCoversEveryClassWhileLabelsAccumulate(t *testing.T) {
	sizes := map[string]int{"c1": 30, "c2": 1, "c3": 5, "c4": 2, "c5": 9, "c6": 1, "c7": 12, "c8": 1}
	pool := dropPool(sizes)
	for _, start := range []time.Time{time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 14, 0, 0, 0, 0, time.UTC)} {
		labeled := map[posting.Key]Entry{}
		seen := map[string]bool{}
		for i := range 6 { // 2 * ceil(8/3)
			plan := AuditDraw(pool, labeled, WeekSeed(start.AddDate(0, 0, 7*i)), AuditQuotas{Dropped: 3})
			for _, p := range plan.Picks {
				seen[p.Source] = true
				labeled[p.Key] = Entry{}
			}
		}
		if len(seen) != len(sizes) {
			t.Errorf("from %s: %d of %d classes reached with labels accumulating: %v", start.Format("2006-01-02"), len(seen), len(sizes), seen)
		}
	}
}

// The scored deciles are not rotated: they keep the hash order, so this change
// does not move the scored draw.
func TestAuditScoredDecilesKeepHashOrder(t *testing.T) {
	var cands []AuditCandidate
	for i := range 100 {
		cands = append(cands, AuditCandidate{Key: posting.Key("gh:k:" + string(rune('a'+i/26)) + string(rune('a'+i%26))), Score: float64(i)})
	}
	for _, seed := range []string{"2026-W44", "2026-W45", "not-a-week"} {
		got := AuditDraw(cands, nil, seed, AuditQuotas{Scored: 5})
		want := AuditPlan{}
		deciles := map[string][]posting.Key{}
		for i, c := range cands {
			src := "decile:" + itoa(i*10/len(cands)+1)
			deciles[src] = append(deciles[src], c.Key)
		}
		drawGroup(&want, deciles, 5, seed, false)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("seed %q: scored draw rotated:\n%+v\n%+v", seed, got.Picks, want.Picks)
		}
	}
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return "10"
}

// A runaway or many-part method never makes an invalid label source.
func TestClosedSourceIsAlwaysAValidSource(t *testing.T) {
	long := strings.Repeat("m", 200)
	for _, class := range []string{"duplicate:" + long, long, "duplicate:" + strings.Repeat("a:", 100), "dead:" + long} {
		got := AuditCell(AuditCandidate{Closed: true, Class: class})
		if !validSource(got) || !strings.HasPrefix(got, ClosedPrefix) {
			t.Errorf("class of %d bytes -> %q", len(class), got)
		}
	}
	// A single long method is cut to classToken's 40 bytes and stays a stratum.
	if got := AuditCell(AuditCandidate{Closed: true, Class: "duplicate:" + long}); got != "closed:duplicate:"+long[:40] {
		t.Errorf("runaway method: %q", got)
	}
	// Many parts overrun the limit: the status is kept, the method is not.
	if got := AuditCell(AuditCandidate{Closed: true, Class: "duplicate:" + strings.Repeat("a:", 100)}); got != "closed:duplicate:other" {
		t.Errorf("many-part method: %q", got)
	}
}
