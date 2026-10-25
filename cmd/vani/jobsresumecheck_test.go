package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resumestore"
	"github.com/bharathvbcr/Vani/internal/rkbresume"
)

func hex12of(c byte) string { return strings.Repeat(string(c), 12) }
func hex64of(c byte) string { return strings.Repeat(string(c), 64) }

func checkFP() rkbresume.Fingerprints {
	return rkbresume.Fingerprints{Bank: hex64of('a'), BaseSelection: hex64of('b'), Selections: hex64of('c'), VMReport: hex64of('d'), Claims: hex64of('e'), RKB: hex64of('f')}
}

func checkMeta() resumestore.Meta {
	return resumestore.Meta{
		Bullets: []resumestore.BulletRef{{ID: "s1", TextHash: hex12of('1')}, {ID: "b1", TextHash: hex12of('2')}, {ID: "b2", TextHash: hex12of('3')}},
		Prints:  resumestore.Fingerprints(checkFP()),
	}
}

func checkPool() rkbresume.PoolView {
	return rkbresume.PoolView{
		Summary:      []rkbresume.PoolBullet{{ID: "s1", TextHash: hex12of('1')}},
		Entries:      []rkbresume.PoolEntry{{ID: "job", Bullets: []rkbresume.PoolBullet{{ID: "b1", TextHash: hex12of('2')}, {ID: "b2", TextHash: hex12of('3'), Follows: "b1"}}}},
		Fingerprints: checkFP(),
	}
}

var checkBank = map[string]bool{"s1": true, "b1": true, "b2": true}

func TestCheckEligibilityCurrentWhenEveryBulletIsOfferedAsJudged(t *testing.T) {
	got := checkEligibility(checkMeta(), checkPool(), checkBank)
	if got.State != eligCurrent || got.Reworded != nil || got.Gone != nil || got.Ineligible != nil || got.InputsChanged != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckEligibilityNamesEachKindOfFault(t *testing.T) {
	pool := checkPool()
	pool.Summary[0].TextHash = hex12of('9')         // s1 reworded (still offered)
	bank := map[string]bool{"s1": true, "b1": true} // b1 is in the bank but not offered; b2 is gone from the bank
	pool.Entries[0].Bullets = nil
	got := checkEligibility(checkMeta(), pool, bank)
	if got.State != eligStale || !reflect.DeepEqual(got.Reworded, []string{"s1"}) || !reflect.DeepEqual(got.Ineligible, []string{"b1"}) || !reflect.DeepEqual(got.Gone, []string{"b2"}) {
		t.Fatalf("got %+v", got)
	}
	ids := got.staleIDs()
	if len(ids) != 3 || !ids["s1"] || !ids["b1"] || !ids["b2"] {
		t.Errorf("staleIDs = %v", ids)
	}
}

// With no bank to ask, a bullet that is not offered is still unusable: it is
// reported as ineligible, never as fine.
func TestCheckEligibilityWithoutTheBankReportsMissingBulletsAsIneligible(t *testing.T) {
	pool := checkPool()
	pool.Entries[0].Bullets = nil
	got := checkEligibility(checkMeta(), pool, nil)
	if got.State != eligStale || !reflect.DeepEqual(got.Ineligible, []string{"b1", "b2"}) || got.Gone != nil {
		t.Fatalf("got %+v", got)
	}
}

// Only the validator report makes a version stale by itself: the bank, the
// selections and rkb moving is reported, and the bullets say whether it mattered.
func TestCheckEligibilityInputsChanged(t *testing.T) {
	pool := checkPool()
	pool.Fingerprints.Bank, pool.Fingerprints.Claims, pool.Fingerprints.RKB, pool.Fingerprints.Selections = hex64of('0'), hex64of('0'), hex64of('0'), hex64of('0')
	got := checkEligibility(checkMeta(), pool, checkBank)
	if got.State != eligCurrent || !reflect.DeepEqual(got.InputsChanged, []string{"bank", "selections", "claims", "rkb"}) {
		t.Fatalf("got %+v", got)
	}
	pool.Fingerprints.VMReport = hex64of('0')
	got = checkEligibility(checkMeta(), pool, checkBank)
	if got.State != eligStale || got.Why == "" || got.Reworded != nil || !strings.Contains(strings.Join(got.InputsChanged, ","), "vm_report") {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckEligibilityToleratesDuplicateAndEmptyBullets(t *testing.T) {
	m := checkMeta()
	m.Bullets = append(m.Bullets, m.Bullets[0], m.Bullets[0])
	if got := checkEligibility(m, checkPool(), checkBank); got.State != eligCurrent {
		t.Errorf("duplicates: %+v", got)
	}
	m.Bullets = nil
	if got := checkEligibility(m, rkbresume.PoolView{Fingerprints: checkFP()}, nil); got.State != eligCurrent {
		t.Errorf("no bullets: %+v", got)
	}
	pool := checkPool()
	pool.Summary, pool.Entries = nil, nil
	if got := checkEligibility(checkMeta(), pool, checkBank); got.State != eligStale || len(got.Ineligible) != 3 {
		t.Errorf("an empty pool leaves nothing current: %+v", got)
	}
}
