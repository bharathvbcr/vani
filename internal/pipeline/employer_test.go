package pipeline

import (
	"maps"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

type fakeEmployer map[string]EmployerInfo

func (f fakeEmployer) Employer(c string) EmployerInfo { return f[c] }

var acmeCapExempt = fakeEmployer{"Acme": {
	Badge:      "likely H-1B cap-exempt (degree-granting public or non-profit institution, IPEDS) (exact name match)",
	Attributes: []string{"cap_exempt_likely"},
}}

// TestEmployerBadgeNeverChangesStatus runs the same posting through a drop, an
// unsure and a kept answer, with and without an employer badge and a matching
// preference. Only the employer keys and the rank may differ.
func TestEmployerBadgeNeverChangesStatus(t *testing.T) {
	answers := map[string]decide.Answer{
		"dropped": {Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("onsite", 0.99)}},
		"unsure":  {Backend: "jev", Slots: map[string]decide.SlotReading{"remote": {Reading: decide.Abstained}}},
		"kept":    {Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}},
	}
	for name, ans := range answers {
		t.Run(name, func(t *testing.T) {
			plain, pl, pk := runnerWith(t, ans, nil)
			badged, bl, bk := runnerWith(t, ans, nil)
			badged.Employer = acmeCapExempt
			badged.PreferEmployers = []string{"cap_exempt_likely", "nonprofit"}
			if err := plain.Advance(ctx, pk); err != nil {
				t.Fatal(err)
			}
			if err := badged.Advance(ctx, bk); err != nil {
				t.Fatal(err)
			}
			p, b := rec(t, pl, pk), rec(t, bl, bk)
			if p.Status != b.Status {
				t.Fatalf("status %s with a badge, %s without", b.Status, p.Status)
			}
			if b.Annotations[ledger.AnnEmployer] == "" {
				t.Fatalf("employer badge missing: %v", b.Annotations)
			}
			strip := func(m map[string]string) map[string]string {
				m = maps.Clone(m)
				delete(m, ledger.AnnEmployer)
				delete(m, ledger.AnnRank)
				return m
			}
			if !maps.Equal(strip(p.Annotations), strip(b.Annotations)) {
				t.Fatalf("annotations differ beyond employer and rank:\n plain %v\nbadged %v", p.Annotations, b.Annotations)
			}
			if b.Status == posting.Dropped {
				if b.Annotations[ledger.AnnRank] != "" {
					t.Fatal("a dropped posting got a rank")
				}
				return
			}
			if p.Annotations[ledger.AnnRank] != "62.5" || b.Annotations[ledger.AnnRank] != "72.5" || b.Annotations[ledger.AnnCoverage] != "62.5" {
				t.Fatalf("rank plain %q badged %q coverage %q", p.Annotations[ledger.AnnRank], b.Annotations[ledger.AnnRank], b.Annotations[ledger.AnnCoverage])
			}
		})
	}
}

func TestEmployerBoostNeedsPreferenceAndConfidentMatch(t *testing.T) {
	ans := decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}
	cases := map[string]struct {
		lookup fakeEmployer
		prefer []string
		rank   string
	}{
		"no preference":      {acmeCapExempt, nil, "62.5"},
		"other preference":   {acmeCapExempt, []string{"nonprofit"}, "62.5"},
		"weak match":         {fakeEmployer{"Acme": {Badge: "likely non-profit (unique prefix match)"}}, []string{"nonprofit"}, "62.5"},
		"preferred":          {acmeCapExempt, []string{"cap_exempt_likely"}, "72.5"},
		"no match, no badge": {fakeEmployer{}, []string{"cap_exempt_likely"}, "62.5"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r, l, k := runnerWith(t, ans, nil)
			r.Employer, r.PreferEmployers = c.lookup, c.prefer
			if err := r.Advance(ctx, k); err != nil {
				t.Fatal(err)
			}
			got := rec(t, l, k)
			if got.Annotations[ledger.AnnRank] != c.rank || got.Status != posting.Explained {
				t.Fatalf("rank %q status %s, want %s", got.Annotations[ledger.AnnRank], got.Status, c.rank)
			}
			// The ledger holds the badge text only: no attribute list and no
			// trace of which attributes the owner prefers.
			for key := range got.Annotations {
				if strings.HasPrefix(key, "employer.") && key != ledger.AnnEmployer {
					t.Fatalf("unexpected employer key %q in the ledger", key)
				}
			}
			for _, v := range got.Annotations {
				if strings.Contains(v, "cap_exempt_likely") {
					t.Fatalf("a preference value reached the ledger: %v", got.Annotations)
				}
			}
		})
	}
}
