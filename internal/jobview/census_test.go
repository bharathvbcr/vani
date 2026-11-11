package jobview

import (
	"encoding/json"
	"math/rand/v2"
	"testing"

	"github.com/bharathvbcr/Vani/internal/location"
)

// countPlacesOracle is countPlaces as it was before it looked back over the
// earlier places instead of keeping a set per job (9965dcd), over the fields
// it read.
func countPlacesOracle(out *Stats, areas map[string]*AreaStat, places []Place, remoteListed bool, remoteScope string) {
	states, seen := map[string]bool{}, map[string]bool{}
	for _, p := range places {
		if p.State != "" && !states[p.State] {
			states[p.State] = true
			out.State[p.State]++
		}
		a := location.NormArea(p.Area)
		if a == "" {
			continue
		}
		k := a + "\x00" + p.State
		if seen[k] {
			continue
		}
		seen[k] = true
		if areas[k] == nil {
			areas[k] = &AreaStat{Area: p.Area, State: p.State}
		}
		areas[k].N++
	}
	if len(states) == 0 {
		out.State["none"]++
	}
	if remoteListed {
		out.RemoteListed++
		scope := remoteScope
		if scope == "" {
			scope = "unstated"
		}
		out.RemoteScope[scope]++
	}
	cs := countriesIn(places, remoteScope)
	if len(cs) == 0 {
		out.Country["none"]++
		return
	}
	for _, c := range cs {
		out.Country[c]++
	}
}

// The census counts what the pinned oracle counts: each state and each folded
// area once per job, over repeated, case-folded and punctuated areas, empty
// ones, and more places than the look-back buffer holds.
func TestCensusMatchesPinnedOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(37, 41))
	areasIn := []string{"Austin", "austin", "AUSTIN,", " Austin ", "San Francisco", "san-francisco", "Berlin", "St. Louis"}
	statesIn := []string{"", "TX", "CA", "MO"}
	regions := []string{"", "Bavaria", "Texas"}
	scopes := []string{"", "US", "Worldwide", "EU"}
	newStats := func() Stats {
		return Stats{State: map[string]int{}, Country: map[string]int{}, RemoteScope: map[string]int{}}
	}
	got, want := newStats(), newStats()
	gotAreas, wantAreas := areaCounts{}, map[string]*AreaStat{}
	for range 5000 {
		n := rng.IntN(12)
		places := make([]Place, n)
		for i := range places {
			places[i] = Place{Area: areasIn[rng.IntN(len(areasIn))], State: statesIn[rng.IntN(len(statesIn))], Region: regions[rng.IntN(len(regions))]}
			if rng.IntN(6) == 0 {
				places[i].Area, places[i].State = "", statesIn[1+rng.IntN(len(statesIn)-1)] // a state alone
			}
		}
		remote, scope := rng.IntN(2) == 0, scopes[rng.IntN(len(scopes))]
		countPlacesIn(&got, gotAreas, places, remote, scope)
		countPlacesOracle(&want, wantAreas, places, remote, scope)
	}
	got.Area, want.Area = rankedAreas(gotAreas), rankedAreasOracle(wantAreas)
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("census differs from the oracle\ngot:  %s\nwant: %s", gj, wj)
	}
}

// rankedAreasOracle is rankedAreas over the oracle's map.
func rankedAreasOracle(areas map[string]*AreaStat) []AreaStat {
	m := areaCounts{}
	for k, a := range areas {
		m[areaKey{area: k}] = a
	}
	return rankedAreas(m)
}
