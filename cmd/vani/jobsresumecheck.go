package main

import (
	"context"
	"strings"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

// The staleness check of a stored version: is what it prints still vouched for
// by the bank as it stands now? A version records, per bullet, the id and the
// hash of the wording it was chosen for, and the fingerprints of every input
// its build read. The check compares those to a fresh rkbresume.Pool (the
// bullets that are eligible now, with their wording hashes) and the bank's own
// bullet ids. That is eligibility. Whether rkb still double-verifies the figures
// of the bullets is a property of the whole choice (rkb judges it against the
// sentences the choice uses), so each version's own choice is exported once,
// as the build that made it was (rkbresume.PoolAndVerify): a bullet whose figure
// no longer verifies makes the version stale, and a version whose figures could
// not be re-checked is "unknown", never "current". A changed validator report
// alone also makes a version stale (rebuild, or revalidate, to re-check).

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
// (including a continuation whose lead went); Unverified are bullets whose
// figure rkb no longer double-verifies for this version's choice. InputsChanged
// names, by the fingerprint's JSON name, each input that differs from what the
// version read; only the validator report makes a version stale by itself, the
// rest are reported for information (the bullets say whether they mattered).
type resumeEligibility struct {
	State         string   `json:"state"`
	Reworded      []string `json:"reworded,omitempty"`
	Gone          []string `json:"gone,omitempty"`
	Ineligible    []string `json:"ineligible,omitempty"`
	Unverified    []string `json:"unverified,omitempty"`
	InputsChanged []string `json:"inputs_changed,omitempty"`
	Why           string   `json:"why,omitempty"`
}

// staleIDs is every bullet the check found a fault with.
func (r resumeEligibility) staleIDs() map[string]bool {
	bad := map[string]bool{}
	for _, l := range [][]string{r.Reworded, r.Gone, r.Ineligible, r.Unverified} {
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

// foldVerification adds what rkb said of a version's own choice to its bullet
// verdict. Bullets whose figure no longer verifies are listed and make the
// version stale; a choice that could not be verified leaves a current version
// "unknown" (a verdict that was not reached is not a pass). A version already
// stale or unknown keeps that state: the bullets say why, and a verification
// error would only repeat it.
func foldVerification(r resumeEligibility, v rkbresume.Verification) resumeEligibility {
	if len(v.Unverified) > 0 {
		r.State, r.Unverified = eligStale, v.Unverified
		if r.Why == "" {
			r.Why = "rkb no longer double-verifies a figure in " + strings.Join(v.Unverified, ", ") + "; revalidate drops those bullets, or build again"
		}
		return r
	}
	if r.State == eligCurrent && v.Err != "" {
		r.State = eligUnknown
		r.Why = oneLine("the figures of this version could not be re-checked: "+v.Err, 300)
	}
	return r
}

// freshCheck is what the staleness check compares to: the pool as it stands now,
// the bank's bullet ids, and rkb's verdict on each version's own choice (parallel
// to the versions asked about).
type freshCheck struct {
	pool rkbresume.PoolView
	bank map[string]bool
	ver  []rkbresume.Verification
}

// eligibility is version i's fresh verdict: its bullets against the pool, then
// its figures against rkb.
func (fc freshCheck) eligibility(i int, m resumestore.Meta) resumeEligibility {
	el := checkEligibility(m, fc.pool, fc.bank)
	if i < len(fc.ver) {
		return foldVerification(el, fc.ver[i])
	}
	return el
}

// freshCheck reads the pool and re-exports each of metas' choices (one rkb run
// each, no model, no build lock, nothing rendered). A bank that cannot be read
// leaves the ids nil. A pool that cannot be had is the error.
func (e *env) freshCheck(ctx context.Context, w resumeWiring, metas []resumestore.Meta) (freshCheck, error) {
	edits := make([][]byte, len(metas))
	for i, m := range metas {
		edits[i] = m.Selection
	}
	pool, ver, err := rkbresume.PoolAndVerify(ctx, w.RC, edits)
	if err != nil {
		return freshCheck{}, err
	}
	bank, err := kbsync.Load(w.Cfg.KB.Bank)
	if err != nil {
		return freshCheck{pool: pool, ver: ver}, nil
	}
	ids := make(map[string]bool, len(bank.Bullets))
	for id := range bank.Bullets {
		ids[id] = true
	}
	return freshCheck{pool: pool, bank: ids, ver: ver}, nil
}
