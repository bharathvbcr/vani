package main

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var memoLocations = []string{"", "Austin, TX", "Remote - US", "Berlin, Germany", "San Francisco, CA or Remote",
	"N/A", "New York, NY; London, UK", "Remote (EMEA)", "Toronto, ON, Canada"}

// The memo's place answer is jobview.Locate's for every location, remote flag
// and other posting field: Locate reads only Location and Remote, which the
// memo keys on. If Locate starts to read another field, this fails.
func TestRankMemoLocateAnswersAsLocate(t *testing.T) {
	m := newRankMemo()
	for round := range 2 {
		for _, loc := range memoLocations {
			for _, remote := range []bool{false, true} {
				p := posting.Posting{Key: "gh:x:1", Company: "Remote Co", Title: "Engineer in Austin, TX",
					Description: "Remote first; offices in Berlin.", Department: "Remote", Location: loc, Remote: remote}
				var want, got jobview.Job
				jobview.Locate(&want, p)
				m.locate(&got, p)
				if !reflect.DeepEqual(got.Places, want.Places) || got.RemoteListed != want.RemoteListed || got.RemoteScope != want.RemoteScope {
					t.Fatalf("round %d, %q remote %v: memo %+v, Locate %+v", round, loc, remote, got, want)
				}
			}
		}
	}
	lookups, misses := m.stats()
	if want := int64(2 * len(memoLocations) * 2); lookups != want || misses != want/2 {
		t.Fatalf("lookups %d misses %d, want %d and %d: the second round was not answered from the memo", lookups, misses, want, want/2)
	}
}

// Two jobs share a remembered Places slice; an append to one is not seen by
// the other.
func TestRankMemoPlacesAreNotAliased(t *testing.T) {
	m := newRankMemo()
	p := posting.Posting{Location: "New York, NY; London, UK"}
	var a, b jobview.Job
	m.locate(&a, p)
	m.locate(&b, p)
	if len(a.Places) == 0 {
		t.Fatal("setup: no places")
	}
	a.Places = append(a.Places, jobview.Place{Area: "Elsewhere"})
	var c jobview.Job
	m.locate(&c, p)
	if len(b.Places) != len(c.Places) || len(c.Places) == len(a.Places) {
		t.Fatalf("an append to one job's places reached the memo: %+v / %+v", a.Places, c.Places)
	}
}

// A title's rank is read under the signals it is asked with; a change of
// signals is never answered from ranks filled under others.
func TestRankMemoTitleRankFollowsTheSignals(t *testing.T) {
	m := newRankMemo()
	a := config.TitleSignals{Include: []string{"platform", "go"}}
	b := config.TitleSignals{Include: []string{"platform"}, Exclude: []string{"staff"}}
	titles := []string{"Platform Engineer", "Staff Platform Engineer, Go", "Sales", ""}
	for range 2 {
		for _, s := range []config.TitleSignals{a, b, a} {
			rank := m.titleRank(s)
			for _, title := range titles {
				if got, want := rank(title), s.Rank(title); got != want {
					t.Fatalf("signals %+v, %q: memo %d, Rank %d", s, title, got, want)
				}
			}
		}
	}
	if titleSignalsKey(config.TitleSignals{Include: []string{"a\x00b"}}) == titleSignalsKey(config.TitleSignals{Include: []string{"a", "b"}}) {
		t.Fatal("two different include lists share a key")
	}
	if titleSignalsKey(config.TitleSignals{Include: []string{"a"}}) == titleSignalsKey(config.TitleSignals{Exclude: []string{"a"}}) {
		t.Fatal("include and exclude share a key")
	}
}

func TestRankMemoTitleHitsAnswerAsExtract(t *testing.T) {
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	m := newRankMemo()
	for range 2 {
		for _, title := range []string{"Go and Rust Engineer", "Kubernetes Platform", "", "Sales"} {
			if got, want := m.titleHits(v, title), v.Extract(title); !reflect.DeepEqual(got, want) {
				t.Fatalf("%q: memo %+v, Extract %+v", title, got, want)
			}
		}
	}
	if _, misses := m.stats(); misses != 4 {
		t.Fatalf("misses %d, want 4", misses)
	}
}

// Past the cap a map is emptied and refilled: answers stay right.
func TestRankMemoCap(t *testing.T) {
	old := rankMemoCap
	rankMemoCap = 3
	t.Cleanup(func() { rankMemoCap = old })
	m := newRankMemo()
	s := config.TitleSignals{Include: []string{"engineer"}}
	rank := m.titleRank(s)
	for i := range 20 {
		title := fmt.Sprintf("Engineer %d", i%7)
		if got, want := rank(title), s.Rank(title); got != want {
			t.Fatalf("%q: %d, want %d", title, got, want)
		}
		var got, want jobview.Job
		p := posting.Posting{Location: memoLocations[i%len(memoLocations)]}
		m.locate(&got, p)
		jobview.Locate(&want, p)
		if !reflect.DeepEqual(got.Places, want.Places) {
			t.Fatalf("%q: places %+v, want %+v", p.Location, got.Places, want.Places)
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.rank) > 3 || len(m.places) > 3 {
		t.Fatalf("maps past the cap: %d ranks, %d places", len(m.rank), len(m.places))
	}
}

func TestRankMemoIsSafeForConcurrentUse(t *testing.T) {
	v, err := jobinsight.Default()
	if err != nil {
		t.Fatal(err)
	}
	m := newRankMemo()
	s := config.TitleSignals{Include: []string{"engineer"}}
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			rank := m.titleRank(s)
			for i := range 200 {
				title := fmt.Sprintf("Engineer %d", (i+g)%13)
				rank(title)
				m.titleHits(v, title)
				var j jobview.Job
				m.locate(&j, posting.Posting{Location: memoLocations[i%len(memoLocations)]})
			}
		})
	}
	wg.Wait()
}

// A score file rewritten between two requests (a warm) is read again: the
// second reply carries the new scores, as the command's does, and an unchanged
// file is not read again.
func TestJobsServeSeesARewrittenScoreFile(t *testing.T) {
	cfgPath, state := semanticSetup(t)
	t.Run("seed", func(t *testing.T) {
		seedInsight(t, state,
			insightSeed{key: "gh:a:1-sales", company: "Acme", title: "Account Executive", status: posting.Explained,
				desc: "Sell enterprise subscriptions, manage quota and territory accounts", posted: daysAgo(3), seenAgo: time.Hour},
			insightSeed{key: "gh:a:2-infra", company: "Acme", title: "Infrastructure Engineer", status: posting.Explained,
				desc: "Run k8s clusters and golang services in production", posted: daysAgo(3), seenAgo: time.Hour},
		)
	})
	e := &env{stateDir: state, configPath: cfgPath, stdout: io.Discard, stderr: io.Discard}
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)
	ask := func(step string) string {
		t.Helper()
		resp := s.runOp(step, serveOps["list"], []string{"--view", "all", "--sort", "priority"})
		if !resp.OK {
			t.Fatalf("%s: %+v", step, resp.Error)
		}
		if want := cliReply(t, e, (*env).jobsList, "--view", "all", "--sort", "priority"); string(resp.Result) != want {
			t.Fatalf("%s: serve and the command differ: %s", step, firstDiff(string(resp.Result), want))
		}
		return string(resp.Result)
	}
	before := ask("before a warm")
	opens := res.lookups.Opens()
	ask("again")
	if res.lookups.Opens() != opens {
		t.Fatalf("an unchanged score file was read again (%d opens, then %d)", opens, res.lookups.Opens())
	}
	mustCode(t, 0, "semantic", "warm")
	after := ask("after a warm")
	if after == before || !strings.Contains(after, `"semantic"`) {
		t.Fatal("the warm's scores are not in serve's reply")
	}
	if res.lookups.Opens() != opens+1 {
		t.Fatalf("opens %d after a warm, want %d", res.lookups.Opens(), opens+1)
	}

	// A second warm rewrites the file that is already there.
	appendPosting(t, state, insightSeed{key: "gh:a:3-sre", company: "Acme", title: "Site Reliability Engineer",
		status: posting.Explained, desc: "Operate k8s and golang services on call", posted: daysAgo(2), seenAgo: time.Hour})
	ask("a new posting before its warm")
	mustCode(t, 0, "semantic", "warm")
	var l jobview.List
	if err := json.Unmarshal([]byte(ask("after the second warm")), &l); err != nil {
		t.Fatal(err)
	}
	for _, j := range l.Jobs {
		if j.Key == "gh:a:3-sre" && j.Semantic == nil {
			t.Fatal("the second warm's score is not in serve's reply")
		}
	}
}
