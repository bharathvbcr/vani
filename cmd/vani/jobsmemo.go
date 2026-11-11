package main

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// rankMemo remembers, for a resident Jobs reader (`vani jobs serve`), answers
// that are pure functions of one posting field: a title's rank under the
// title signals, a title's keyword hits, and a location's places. Each map is
// keyed by everything its function reads, so an entry is never stale and
// nothing here needs to know when the ledger, the marks or the clock change:
// a new title or location is simply a new key. The answers are shared between
// requests and must not be changed by a reader.
//
// Each map holds at most rankMemoCap entries; past that it is emptied and
// fills again, which costs time, never an answer.
type rankMemo struct {
	mu sync.RWMutex

	signals string // titleSignalsKey of the signals rank was filled under
	rank    map[string]int

	vocab string // Fingerprint of the vocabulary hits was filled from
	hits  map[string][]jobinsight.Hit

	places map[placeKey]placeAnswer

	// lookups counts every question asked and misses those computed, for tests.
	lookups, misses atomic.Int64
}

var rankMemoCap = 1 << 18 // a var so a test can lower it

type placeKey struct {
	location string
	remote   bool
}

// placeAnswer is what jobview.Locate sets on a job.
type placeAnswer struct {
	places       []jobview.Place
	remoteListed bool
	remoteScope  string
}

func newRankMemo() *rankMemo {
	return &rankMemo{rank: map[string]int{}, hits: map[string][]jobinsight.Hit{}, places: map[placeKey]placeAnswer{}}
}

// titleSignalsKey is everything TitleSignals.Rank reads, each phrase quoted so
// no two lists share a key.
func titleSignalsKey(s config.TitleSignals) string {
	return fmt.Sprintf("%q %q", s.Include, s.Exclude)
}

// titleRank is s.Rank, remembered per title.
func (m *rankMemo) titleRank(s config.TitleSignals) func(string) int {
	key := titleSignalsKey(s)
	return func(title string) int {
		m.mu.RLock()
		v, ok := m.rank[title]
		ok = ok && m.signals == key
		m.mu.RUnlock()
		if ok {
			m.lookups.Add(1)
			return v
		}
		v = s.Rank(title)
		m.mu.Lock()
		if m.signals != key || len(m.rank) >= rankMemoCap {
			m.rank, m.signals = map[string]int{}, key
		}
		m.rank[title] = v
		m.mu.Unlock()
		m.miss()
		return v
	}
}

// titleHits is v.Extract(title), remembered per title.
func (m *rankMemo) titleHits(v *jobinsight.Vocab, title string) []jobinsight.Hit {
	fp := v.Fingerprint()
	m.mu.RLock()
	h, ok := m.hits[title]
	ok = ok && m.vocab == fp
	m.mu.RUnlock()
	if ok {
		m.lookups.Add(1)
		return h
	}
	h = slices.Clip(v.Extract(title))
	m.mu.Lock()
	if m.vocab != fp || len(m.hits) >= rankMemoCap {
		m.hits, m.vocab = map[string][]jobinsight.Hit{}, fp
	}
	m.hits[title] = h
	m.mu.Unlock()
	m.miss()
	return h
}

// locate is jobview.Locate(j, p), remembered per location text and remote
// flag (the only fields it reads). j.Places is shared and clipped, so an
// append to it copies.
func (m *rankMemo) locate(j *jobview.Job, p posting.Posting) {
	k := placeKey{p.Location, p.Remote}
	m.mu.RLock()
	a, ok := m.places[k]
	m.mu.RUnlock()
	if ok {
		m.lookups.Add(1)
	} else {
		var probe jobview.Job
		jobview.Locate(&probe, posting.Posting{Location: p.Location, Remote: p.Remote})
		a = placeAnswer{places: slices.Clip(probe.Places), remoteListed: probe.RemoteListed, remoteScope: probe.RemoteScope}
		m.mu.Lock()
		if len(m.places) >= rankMemoCap {
			m.places = map[placeKey]placeAnswer{}
		}
		m.places[k] = a
		m.mu.Unlock()
		m.miss()
	}
	j.Places, j.RemoteListed, j.RemoteScope = a.places, a.remoteListed, a.remoteScope
}

// miss counts a question that was computed.
func (m *rankMemo) miss() {
	m.lookups.Add(1)
	m.misses.Add(1)
}

// stats is how many questions were asked and how many were computed.
func (m *rankMemo) stats() (lookups, misses int64) {
	return m.lookups.Load(), m.misses.Load()
}
