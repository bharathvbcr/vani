package eval

import (
	"fmt"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func strataCands() []AuditCandidate {
	var c []AuditCandidate
	for i := 0; i < 47; i++ {
		c = append(c, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:s:%02d", i)), Score: float64(i%13) + float64(i)/100})
	}
	for i := 0; i < 6; i++ {
		c = append(c, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:d:%02d", i)), Dropped: true, Class: []string{"model:sponsorship", "exclude:title", ""}[i%3]})
	}
	for i := 0; i < 4; i++ {
		c = append(c, AuditCandidate{Key: posting.Key(fmt.Sprintf("gh:c:%02d", i)), Closed: true, Class: []string{"dead", "duplicate"}[i%2]})
	}
	return c
}

// The strata AuditStratumOf reports are the ones AuditDraw draws from: with
// quotas big enough to draw everything, each pick's stratum is the one named.
func TestAuditStrataAreTheDrawsStrata(t *testing.T) {
	cands := strataCands()
	want := AuditStratumOf(cands)
	if len(want) != len(cands) {
		t.Fatalf("%d strata for %d candidates", len(want), len(cands))
	}
	plan := AuditDraw(cands, nil, "w", AuditQuotas{Scored: 100, Dropped: 100, Closed: 100})
	if len(plan.Picks) != len(cands) {
		t.Fatalf("drew %d of %d", len(plan.Picks), len(cands))
	}
	for _, p := range plan.Picks {
		if want[p.Key] != p.Source {
			t.Errorf("%s: AuditStratumOf %q, draw %q", p.Key, want[p.Key], p.Source)
		}
	}
}

// A labelled posting still has its stratum (deciles are cut over the whole
// scored pool), a repeated key keeps its first entry, and input order does not
// matter.
func TestAuditStrataCoverLabelledAndRepeatedKeys(t *testing.T) {
	cands := strataCands()
	rev := make([]AuditCandidate, len(cands))
	for i, c := range cands {
		rev[len(cands)-1-i] = c
	}
	a, b := AuditStratumOf(cands), AuditStratumOf(rev)
	for k, s := range a {
		if b[k] != s {
			t.Errorf("%s: %q in one order, %q in the other", k, s, b[k])
		}
	}
	dup := append(append([]AuditCandidate(nil), cands...), AuditCandidate{Key: cands[0].Key, Dropped: true, Class: "x"}, AuditCandidate{})
	if got := AuditStratumOf(dup); len(got) != len(a) || got[cands[0].Key] != a[cands[0].Key] {
		t.Errorf("a repeated key or an empty key changed the strata: %v", got[cands[0].Key])
	}
	if got := AuditStratumOf(nil); len(got) != 0 {
		t.Errorf("no candidates gave %v", got)
	}
	// Ten deciles over 47 scored postings.
	deciles := map[string]int{}
	for k, s := range a {
		if k[:4] == "gh:s" {
			deciles[s]++
		}
	}
	if len(deciles) != 10 {
		t.Errorf("%d deciles: %v", len(deciles), deciles)
	}
}
