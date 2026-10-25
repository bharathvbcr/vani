package main

import (
	"context"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// The staleness check of a stored version: is what it prints still vouched for
// by the bank as it stands now? A version records, per bullet, the id and the
// hash of the wording it was chosen for, and the fingerprints of every input
// its build read. The check compares those to a fresh rkbresume.Pool (the
// bullets that are eligible now, with their wording hashes) and the bank's own
// bullet ids. It judges eligibility only; whether a figure rkb has not
// double-verified would be dropped is decided when a page is built, which is
// why a changed validator report alone makes a version stale (rebuild, or
// revalidate, to re-check the figures).

// Fresh eligibility states. A check that could not run is "unknown" and is never
// read as "current".
const (
	eligCurrent = "current"
	eligStale   = "stale"
	eligUnknown = "unknown"
)

// resumeEligibility is a version's fresh eligibility. Reworded are the ids whose
// wording changed since the version was built (the new wording is eligible, but
// the version was not judged on it); Gone are ids the bank no longer has;
// Ineligible are ids still in the bank that the pool no longer offers
// (including a continuation whose lead went). InputsChanged names, by the
// fingerprint's JSON name, each input that differs from what the version read;
// only the validator report makes a version stale by itself, the rest are
// reported for information (the bullets say whether they mattered).
type resumeEligibility struct {
	State         string   `json:"state"`
	Reworded      []string `json:"reworded,omitempty"`
	Gone          []string `json:"gone,omitempty"`
	Ineligible    []string `json:"ineligible,omitempty"`
	InputsChanged []string `json:"inputs_changed,omitempty"`
	Why           string   `json:"why,omitempty"`
}

// staleIDs is every bullet the check found a fault with, in page order.
func (r resumeEligibility) staleIDs() map[string]bool {
	bad := map[string]bool{}
	for _, l := range [][]string{r.Reworded, r.Gone, r.Ineligible} {
		for _, id := range l {
			bad[id] = true
		}
	}
	return bad
}

func unknownEligibility(why string) resumeEligibility {
	return resumeEligibility{State: eligUnknown, Why: oneLine(why, 300)}
}

// poolIndex maps every bullet the pool offers, summary or entry, to itself.
func poolIndex(p rkbresume.PoolView) map[string]rkbresume.PoolBullet {
	idx := map[string]rkbresume.PoolBullet{}
	for _, b := range p.Summary {
		idx[b.ID] = b
	}
	for _, en := range p.Entries {
		for _, b := range en.Bullets {
			idx[b.ID] = b
		}
	}
	return idx
}

// changedInputs names the fingerprints of a differing from b.
func changedInputs(a, b resumestore.Fingerprints) []string {
	var out []string
	for _, f := range []struct{ name, a, b string }{
		{"bank", a.Bank, b.Bank},
		{"base_selection", a.BaseSelection, b.BaseSelection},
		{"selections", a.Selections, b.Selections},
		{"vm_report", a.VMReport, b.VMReport},
		{"claims", a.Claims, b.Claims},
		{"rkb", a.RKB, b.RKB},
	} {
		if f.a != f.b {
			out = append(out, f.name)
		}
	}
	return out
}

// checkEligibility judges version m against a fresh pool. bank is the set of
// bullet ids the bank holds, or nil when it could not be read (a missing bullet
// is then reported as ineligible: still not usable, only less precisely named).
func checkEligibility(m resumestore.Meta, pool rkbresume.PoolView, bank map[string]bool) resumeEligibility {
	idx := poolIndex(pool)
	r := resumeEligibility{State: eligCurrent, InputsChanged: changedInputs(m.Prints, resumestore.Fingerprints(pool.Fingerprints))}
	seen := map[string]bool{}
	for _, b := range m.Bullets {
		if seen[b.ID] {
			continue
		}
		seen[b.ID] = true
		pb, offered := idx[b.ID]
		switch {
		case offered && pb.TextHash == b.TextHash:
		case offered:
			r.Reworded = append(r.Reworded, b.ID)
		case bank != nil && !bank[b.ID]:
			r.Gone = append(r.Gone, b.ID)
		default:
			r.Ineligible = append(r.Ineligible, b.ID)
		}
	}
	reportMoved := false
	for _, n := range r.InputsChanged {
		reportMoved = reportMoved || n == "vm_report"
	}
	if len(r.Reworded)+len(r.Gone)+len(r.Ineligible) > 0 || reportMoved {
		r.State = eligStale
	}
	if reportMoved && len(r.Reworded)+len(r.Gone)+len(r.Ineligible) == 0 {
		r.Why = "the validator report changed, so which figures are verified may have changed; rebuild or revalidate to re-check them"
	}
	return r
}

// freshPool is what the staleness check compares to: the pool as it stands now
// (one rkb run, no model, no build lock) and the bank's bullet ids. A bank that
// cannot be read leaves the ids nil. A pool that cannot be had is the error.
func (e *env) freshPool(ctx context.Context, w resumeWiring) (rkbresume.PoolView, map[string]bool, error) {
	pool, err := rkbresume.Pool(ctx, w.RC, rkbresume.Request{})
	if err != nil {
		return rkbresume.PoolView{}, nil, err
	}
	bank, err := kbsync.Load(w.Cfg.KB.Bank)
	if err != nil {
		return pool, nil, nil
	}
	ids := make(map[string]bool, len(bank.Bullets))
	for id := range bank.Bullets {
		ids[id] = true
	}
	return pool, ids, nil
}
