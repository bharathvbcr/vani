package main

import (
	"hash/maphash"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/jobfacts"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani jobs serve` keeps three parts of a Jobs view's read between requests,
// each reused only while everything it was built from is what this request
// would build it from again:
//
//   - the text facts map (jobsListRead): keyed by the records' generation, the
//     marked postings (which decide the text scope), and the facts store's
//     edit count, sampled before the lookups and again after the read's own
//     prune and save. It is kept only when the read needed no text and left
//     nothing unsaved, so a reuse skips no hydration, derivation or save.
//   - the learned company blurbs and keyword hits (insight.learn): keyed by
//     the kept facts map (so learn reuses only over kept facts), the
//     generation, the postings in view and the vocabulary. Kept only when
//     learning noted no posting missing.
//   - the owner's manual readings (manualReadings): keyed by a stat of the
//     readings file taken before it is read (fileStamp). A writer appends to
//     the file or renames a new one over it, and either changes the stamp.
//
// What is kept is shared by concurrent requests and never changed after it is
// kept; each request wraps it in its own per-request state.

// readKeep holds the kept parts. The zero value is ready.
type readKeep struct {
	seedOnce sync.Once
	seed     maphash.Seed

	mu     sync.Mutex
	facts  *keptFacts
	learnt *keptLearn
	manual *keptManual

	// Hits and misses per part, for tests and the bench.
	factsHits, factsMisses   atomic.Int64
	learnHits, learnMisses   atomic.Int64
	manualHits, manualMisses atomic.Int64
}

// hashSeed is the keep's seed for its set hashes: random per process, so
// posting keys (from job boards) cannot be chosen to collide.
func (k *readKeep) hashSeed() maphash.Seed {
	k.seedOnce.Do(func() { k.seed = maphash.MakeSeed() })
	return k.seed
}

// setHash is an order-free hash of a set of keys: the sum of each key's hash,
// beside the count. Callers pass each key once.
type setHash struct {
	seed maphash.Seed
	sum  uint64
	n    int
}

func (s *setHash) add(k posting.Key) {
	s.sum += maphash.String(s.seed, string(k))
	s.n++
}

// factsKey is what a read's facts map was built from.
type factsKey struct {
	gen     uint64
	marked  uint64 // setHash of the postings with a mark (jobsTextScope)
	nMarked int
	changes uint64 // jobfacts.Store.Changes when the map was built
	vocab   string
}

type keptFacts struct {
	key    factsKey
	m      map[posting.Key]jobfacts.Facts
	served int
}

// markedKey is the setHash of the marks jobsTextScope reads: the marked
// postings whose record (recs, sorted by key, as a snapshot is) has a status
// whose mark brings its text in (scopeReadsMark). With the generation it fixes
// the scope, so saving or hiding a live posting keeps the facts map.
func (k *readKeep) markedKey(m map[posting.Key]marks.Entry, recs []ledger.Record) (uint64, int) {
	h := setHash{seed: k.hashSeed()}
	for key, e := range m {
		if e.Mark == marks.None {
			continue
		}
		i, ok := slices.BinarySearchFunc(recs, key, func(r ledger.Record, k posting.Key) int { return strings.Compare(string(r.Key), string(k)) })
		if ok && scopeReadsMark(recs[i].Status) {
			h.add(key)
		}
	}
	return h.sum, h.n
}

// keptFactsFor is the kept facts under key, nil when there are none.
func (k *readKeep) keptFactsFor(key factsKey) *keptFacts {
	k.mu.Lock()
	defer k.mu.Unlock()
	if f := k.facts; f != nil && f.key == key {
		k.factsHits.Add(1)
		return f
	}
	k.factsMisses.Add(1)
	return nil
}

func (k *readKeep) keepFacts(f *keptFacts) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.facts = f
}

// learnKey is what insight.learn read.
type learnKey struct {
	facts *keptFacts // the facts map hits came from
	gen   uint64
	view  uint64 // setHash of the postings in view "all"
	nView int
	vocab string
}

type keptLearn struct {
	key    learnKey
	hits   map[posting.Key][]jobinsight.Hit
	boiler map[string]map[string]bool
	n      int // InsightInfo.Boilerplate
}

// viewKey is the setHash of the records learn reads: those in view "all".
func (k *readKeep) viewKey(recs []ledger.Record, m map[posting.Key]marks.Entry) (uint64, int) {
	h := setHash{seed: k.hashSeed()}
	for _, r := range recs {
		if inLearnView(r, m) {
			h.add(r.Key)
		}
	}
	return h.sum, h.n
}

func (k *readKeep) keptLearnFor(key learnKey) *keptLearn {
	k.mu.Lock()
	defer k.mu.Unlock()
	if l := k.learnt; l != nil && l.key == key {
		k.learnHits.Add(1)
		return l
	}
	k.learnMisses.Add(1)
	return nil
}

func (k *readKeep) keepLearn(l *keptLearn) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.learnt = l
}

type keptManual struct {
	stamp fileStamp
	m     map[posting.Key]manualtriage.Reading
	fp    uint64 // manualFingerprint of m
}

// manualReadings is manualReadings (jobsretriage.go) reusing the readings
// while their file's stamp is unchanged. The set it returns is this request's
// own; its map is shared and read only.
func (k *readKeep) manualReadings(stateDir string) (*manualReadingSet, func(ledger.Record, string) (string, bool), string) {
	dir := manualtriage.Dir(stateDir)
	st, stampOK := stampOf(manualtriage.File(dir))
	if stampOK {
		k.mu.Lock()
		kept := k.manual
		k.mu.Unlock()
		if kept != nil && kept.stamp == st {
			k.manualHits.Add(1)
			return keptManualSet(kept)
		}
	}
	k.manualMisses.Add(1)
	m, err := manualtriage.Read(dir)
	if err != nil {
		return nil, nil, manualReadError(err)
	}
	kept := &keptManual{stamp: st, m: m}
	kept.fp = manualFingerprint(&manualReadingSet{m: m})
	if stampOK {
		k.mu.Lock()
		k.manual = kept
		k.mu.Unlock()
	}
	return keptManualSet(kept)
}

// keptManualSet is a request's set over kept: nil when there are no readings,
// as manualReadings answers.
func keptManualSet(kept *keptManual) (*manualReadingSet, func(ledger.Record, string) (string, bool), string) {
	if len(kept.m) == 0 {
		return nil, nil, ""
	}
	s := &manualReadingSet{m: kept.m, fp: kept.fp}
	return s, s.value, ""
}
