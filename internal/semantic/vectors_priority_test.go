package semantic

import (
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Eviction order of the chunk-vector log: outside the live set first, then the
// lowest priority (SetVectorPriority), then the oldest written. At the bound a
// posting ranked below everything it would evict is refused.

const (
	primN, primDim = 2, 16
	// primSize is the bytes of one record of primN chunks of primDim floats.
	primSize = 4 + recFixed + 4*primN*primDim + 4
)

func primHash(i int) [16]byte {
	var h [16]byte
	h[0], h[1] = byte(i), byte(i>>8)
	return h
}

func primVecs(n, dim int) [][]float32 {
	out := make([][]float32, n)
	for i := range out {
		v := make([]float32, dim)
		v[i%dim] = 1
		out[i] = v
	}
	return out
}

func primRec(i int) vecRec { return vecRec{h: primHash(i), vecs: primVecs(primN, primDim)} }

// primStore opens a store whose bound holds exactly slots records of primSize.
// ranks is the live set and priorities the compaction sees (nil: none named).
func primStore(t *testing.T, slots int, ranks map[[16]byte]float64) *chunkStore {
	t.Helper()
	s := newChunkStore(logPath(t.TempDir()), "model-x", MaxVectorBytes)
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	s.maxBytes = s.hdrLen + int64(slots)*primSize
	if ranks != nil {
		s.prioFn = func() vecPrio { return vecPrio{rank: ranks} }
	}
	return s
}

func primAppend(t *testing.T, s *chunkStore, ids ...int) {
	t.Helper()
	for _, i := range ids {
		if n, ev, err := s.append([]vecRec{primRec(i)}); n != 1 || err != nil || ev.Refused != 0 {
			t.Fatalf("append %d: n=%d refused=%d err=%v", i, n, ev.Refused, err)
		}
	}
}

func primHas(s *chunkStore, ids ...int) (have, lack []int) {
	for _, i := range ids {
		if _, ok := s.idx[primHash(i)]; ok {
			have = append(have, i)
		} else {
			lack = append(lack, i)
		}
	}
	return have, lack
}

func wantSet(t *testing.T, what string, got []int, want ...int) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %v, want %v", what, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: %v, want %v", what, got, want)
			return
		}
	}
}

func TestVectorsAtTheBoundALowerRankedPostingIsRefused(t *testing.T) {
	ranks := map[[16]byte]float64{primHash(1): 4, primHash(2): 3, primHash(3): 2, primHash(4): 1}
	s := primStore(t, 4, ranks)
	primAppend(t, s, 1, 2, 3, 4)
	gen := s.gen

	// 0.5 is below every stored posting: refused, and nothing is evicted for it.
	ranks[primHash(5)] = 0.5
	n, ev, err := s.append([]vecRec{primRec(5)})
	if n != 0 || ev.Refused != 1 || !errors.Is(err, ErrVectorsFull) {
		t.Fatalf("n=%d refused=%d err=%v", n, ev.Refused, err)
	}
	if ev.Compacted || ev.Evicted != 0 || s.gen != gen {
		t.Errorf("a refused posting compacted the log: %+v", ev)
	}
	have, _ := primHas(s, 1, 2, 3, 4)
	wantSet(t, "stored after the refusal", have, 1, 2, 3, 4)

	// 2.5 outranks the two lowest: it evicts them (lowest first), never the better
	// two. Same session: a rank refusal does not close the log to higher ranks.
	ranks[primHash(6)] = 2.5
	n, ev, err = s.append([]vecRec{primRec(6)})
	if n != 1 || err != nil || ev.Refused != 0 || ev.Evicted != 2 {
		t.Fatalf("n=%d ev=%+v err=%v", n, ev, err)
	}
	have, lack := primHas(s, 1, 2, 3, 4, 6)
	wantSet(t, "stored after the higher-ranked posting", have, 1, 2, 6)
	wantSet(t, "evicted", lack, 3, 4)
	if s.end > s.maxBytes {
		t.Errorf("log %d passes the bound %d", s.end, s.maxBytes)
	}
}

func TestVectorsBatchAdmitsHighestRankFirstWhateverTheOrder(t *testing.T) {
	ranks := map[[16]byte]float64{primHash(1): 5, primHash(2): 5, primHash(8): 1, primHash(9): 9}
	s := primStore(t, 3, ranks)
	primAppend(t, s, 1, 2)
	// Room for one more record after eviction: the low one comes first in the batch.
	n, ev, err := s.append([]vecRec{primRec(8), primRec(9)})
	if n != 1 || ev.Refused != 1 || !errors.Is(err, ErrVectorsFull) {
		t.Fatalf("n=%d refused=%d err=%v", n, ev.Refused, err)
	}
	if have, _ := primHas(s, 9); len(have) != 1 {
		t.Error("the highest-ranked posting was not stored")
	}
	if have, _ := primHas(s, 8); len(have) != 0 {
		t.Error("the lowest-ranked posting was stored over higher-ranked ones")
	}
	if s.end > s.maxBytes {
		t.Errorf("log %d passes the bound %d", s.end, s.maxBytes)
	}
}

func TestVectorsOutsideTheLiveSetGoBeforeAnyRankedPosting(t *testing.T) {
	// 7 is written last (so not the oldest) and is not live; 3 is the lowest
	// ranked and was written third, so neither oldest-first nor newest-first
	// would pick the pair that goes.
	ranks := map[[16]byte]float64{
		primHash(3): 0, primHash(4): 1, primHash(5): 2, primHash(6): 3, primHash(2): 4, primHash(9): 10,
	}
	s := primStore(t, 6, ranks)
	primAppend(t, s, 4, 6, 3, 5, 2, 7)
	n, ev, err := s.append([]vecRec{primRec(9)})
	if n != 1 || err != nil || ev.Evicted != 2 {
		t.Fatalf("n=%d ev=%+v err=%v", n, ev, err)
	}
	have, lack := primHas(s, 2, 3, 4, 5, 6, 7, 9)
	wantSet(t, "kept", have, 2, 4, 5, 6, 9)
	wantSet(t, "evicted: the outsider, then the lowest rank", lack, 3, 7)
}

func TestVectorsWithoutRanksTheOldestGoFirst(t *testing.T) {
	s := primStore(t, 3, nil)
	primAppend(t, s, 1, 2, 3)
	n, ev, err := s.append([]vecRec{primRec(4)})
	if n != 1 || err != nil || ev.Evicted != 2 {
		t.Fatalf("n=%d ev=%+v err=%v", n, ev, err)
	}
	have, lack := primHas(s, 1, 2, 3, 4)
	wantSet(t, "kept", have, 3, 4)
	wantSet(t, "evicted", lack, 1, 2)
}

func TestVectorsEqualRanksFallBackToAge(t *testing.T) {
	ranks := map[[16]byte]float64{}
	for i := 1; i <= 4; i++ {
		ranks[primHash(i)] = 7
	}
	ranks[primHash(5)] = 7
	s := primStore(t, 4, ranks)
	primAppend(t, s, 1, 2, 3, 4)
	n, _, err := s.append([]vecRec{primRec(5)})
	if n != 1 || err != nil {
		t.Fatalf("an equal rank is not lower: n=%d err=%v", n, err)
	}
	have, lack := primHas(s, 1, 2, 3, 4, 5)
	wantSet(t, "kept", have, 3, 4, 5)
	wantSet(t, "evicted", lack, 1, 2)
}

func TestVectorsNaNRanksLowest(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	m.SetVectorPriority(map[string]float64{"a": math.NaN(), "b": 1})
	if r := m.vecPrio["a"]; !math.IsInf(r, -1) || m.vecPrio["b"] != 1 {
		t.Errorf("NaN stored as %v", r)
	}
	ranks := map[[16]byte]float64{primHash(1): math.Inf(-1), primHash(2): 0, primHash(3): 0, primHash(4): 1}
	s := primStore(t, 3, ranks)
	primAppend(t, s, 1, 2, 3)
	if n, _, err := s.append([]vecRec{primRec(4)}); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if have, _ := primHas(s, 1); len(have) != 0 {
		t.Error("the NaN-ranked posting outlived better ones")
	}
}

func TestVectorsEmptyLiveSetEvictsEverythingStoredNotTheIncoming(t *testing.T) {
	s := primStore(t, 3, nil)
	primAppend(t, s, 1, 2, 3)
	s.prioFn = func() vecPrio { return vecPrio{rank: map[[16]byte]float64{}} }
	n, ev, err := s.append([]vecRec{primRec(4)})
	if n != 1 || err != nil || ev.Evicted != 3 {
		t.Fatalf("n=%d ev=%+v err=%v", n, ev, err)
	}
	have, _ := primHas(s, 1, 2, 3, 4)
	wantSet(t, "kept", have, 4)
}

func TestVectorsOversizedRecordIsRefusedAndEvictsNothing(t *testing.T) {
	ranks := map[[16]byte]float64{primHash(1): 1, primHash(2): 1, primHash(3): 1}
	s := primStore(t, 3, ranks)
	primAppend(t, s, 1, 2, 3)
	gen := s.gen
	big := vecRec{h: primHash(50), vecs: primVecs(MaxPostingChunks, primDim)}
	n, ev, err := s.append([]vecRec{big})
	if n != 0 || ev.Refused != 1 || ev.Evicted != 0 || ev.Compacted || s.gen != gen || !errors.Is(err, ErrVectorsFull) {
		t.Fatalf("n=%d ev=%+v err=%v", n, ev, err)
	}
	if have, _ := primHas(s, 1, 2, 3); len(have) != 3 {
		t.Errorf("stored records were evicted for one that can never fit: %v", have)
	}
}

func TestVectorsDuplicateAndMalformedRecordsInABatch(t *testing.T) {
	s := primStore(t, 4, nil)
	bad := vecRec{h: primHash(9), vecs: nil}
	n, ev, err := s.append([]vecRec{primRec(1), primRec(1), bad})
	if n != 1 || ev.Refused != 1 || err == nil {
		t.Fatalf("n=%d refused=%d err=%v", n, ev.Refused, err)
	}
	if n, ev, err = s.append([]vecRec{primRec(1)}); n != 0 || ev.Refused != 0 || err != nil {
		t.Errorf("an unchanged posting is not a refusal: n=%d refused=%d err=%v", n, ev.Refused, err)
	}
}

// Whatever the sequence, the log never passes its bound, an append that stores
// nothing evicts nothing, and an eviction never takes a posting that outranks
// every record the append stored.
func TestVectorsBoundAndEvictionOrderUnderRandomSequences(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	ranks := map[[16]byte]float64{}
	s := newChunkStore(logPath(t.TempDir()), "model-x", MaxVectorBytes)
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.maxBytes = s.hdrLen + 20*primSize
	s.prioFn = func() vecPrio { return vecPrio{rank: ranks} }
	next, stored, refused := 0, 0, 0
	for step := 0; step < 400; step++ {
		if step%25 == 0 {
			if _, err := s.refresh(); err != nil { // a new Warm
				t.Fatal(err)
			}
		}
		var batch []vecRec
		for k := 0; k < 1+rng.IntN(3); k++ {
			next++
			h := primHash(next)
			ranks[h] = float64(rng.IntN(6)) // ties on purpose
			batch = append(batch, vecRec{h: h, vecs: primVecs(1+rng.IntN(4), primDim)})
		}
		if step%40 == 7 { // part of the live set leaves
			for h := range ranks {
				if rng.IntN(5) == 0 {
					delete(ranks, h)
				}
			}
			for _, r := range batch {
				ranks[r.h] = float64(rng.IntN(6))
			}
		}
		before := map[[16]byte]vecLoc{}
		for h, l := range s.idx {
			before[h] = l
		}
		n, ev, _ := s.append(batch)
		stored += n
		refused += ev.Refused
		if n+ev.Refused != len(batch) {
			t.Fatalf("step %d: %d stored + %d refused != %d", step, n, ev.Refused, len(batch))
		}
		if st, err := os.Stat(s.path); err != nil || st.Size() != s.end || s.end > s.maxBytes {
			t.Fatalf("step %d: file %v (%v), index end %d, bound %d", step, st, err, s.end, s.maxBytes)
		}
		if n == 0 && (ev.Evicted != 0 || ev.Compacted) {
			t.Fatalf("step %d: nothing stored yet %+v", step, ev)
		}
		top := math.Inf(-1)
		for _, r := range batch {
			if _, ok := s.idx[r.h]; ok {
				top = max(top, ranks[r.h])
			}
		}
		for h := range before {
			if _, ok := s.idx[h]; ok {
				continue
			}
			if r, live := ranks[h]; live && n > 0 && r > top {
				t.Fatalf("step %d: evicted a posting ranked %v above every stored one (%v)", step, r, top)
			}
		}
	}
	if stored == 0 || refused == 0 {
		t.Fatalf("the sequence never hit the bound: stored %d refused %d", stored, refused)
	}
	// What the index says is what the file holds.
	re := newChunkStore(s.path, "model-x", s.maxBytes)
	if _, err := re.refresh(); err != nil {
		t.Fatal(err)
	}
	defer re.close()
	if len(re.idx) != len(s.idx) || re.live != s.live {
		t.Errorf("reopened index has %d records / %d bytes, the writer's %d / %d", len(re.idx), re.live, len(s.idx), s.live)
	}
}

// A compaction moves records one at a time: its allocation does not grow with
// the log.
func TestVectorsCompactionStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a 30 MB log")
	}
	const dim, recs = 768, 400
	ranks := map[[16]byte]float64{}
	s := newChunkStore(logPath(t.TempDir()), "model-x", 64<<20)
	if _, err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	defer s.close()
	s.prioFn = func() vecPrio { return vecPrio{rank: ranks} }
	for i := 0; i < recs; i += 20 {
		batch := make([]vecRec, 0, 20)
		for k := i; k < i+20; k++ {
			ranks[primHash(k)] = float64(k % 7)
			batch = append(batch, vecRec{h: primHash(k), vecs: primVecs(MaxPostingChunks, dim)})
		}
		if n, _, err := s.append(batch); n != 20 || err != nil {
			t.Fatalf("setup: n=%d err=%v", n, err)
		}
	}
	size := s.end
	if size < 28<<20 {
		t.Fatalf("fixture: log is %d bytes", size)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ev, err := s.compact(vecPrio{rank: ranks}, evictTo(size/2))
	runtime.ReadMemStats(&after)
	if err != nil || !ev.Compacted || ev.Evicted < recs/3 || s.end > size/2 {
		t.Fatalf("ev=%+v err=%v end %d of %d", ev, err, s.end, size)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 8<<20 {
		t.Errorf("compacting a %d MB log allocated %d MB: it buffers the file", size>>20, alloc>>20)
	}
}

// ---- the Matcher hook ----

func TestSetVectorPriorityCopiesAndRanksTheWarmsOwnDocsHighest(t *testing.T) {
	docs := vecDocs(3)
	m, _ := newMatcher(t, newSpy())
	rank := map[string]float64{TextHash(docs[0].Title, docs[0].Description): 2}
	m.SetVectorPriority(rank)
	rank[TextHash(docs[1].Title, docs[1].Description)] = 9 // after the call: must not show
	p := m.vecPrioFn(docs[1:])()
	if r, ok := p.rank[hashOf(docs[0])]; !ok || r != 2 {
		t.Errorf("ranked posting: %v %v", r, ok)
	}
	if r := p.rank[hashOf(docs[1])]; !math.IsInf(r, 1) {
		t.Errorf("a doc of the Warm the map does not rank has %v, want +Inf (kept)", r)
	}
	if _, ok := p.rank[hashOf(docs[2])]; !ok {
		t.Error("the Warm's own posting is not live")
	}
	m.SetVectorPriority(nil)
	if m.vecPrioFn(docs) != nil {
		t.Error("nil names no live set")
	}
}

// Warm A (high priority), then B (low), at a bound that holds A and half of B:
// B's vectors are the ones that go; with no ranks, A's (the oldest) go instead.
func TestVectorsLowPriorityGoesNotTheOldest(t *testing.T) {
	docs := vecDocs(23)
	a, b, c := docs[:10], docs[10:20], docs[20:]
	probe, _ := newMatcher(t, newSpy())
	mustWarm(t, probe, docs...)
	sum := func(ds []Doc) (n int64) {
		for _, d := range ds {
			n += probe.vs.idx[hashOf(d)].size()
		}
		return n
	}
	bound := probe.vs.hdrLen + sum(a) + sum(b)/2
	ranked := func() map[string]float64 {
		r := map[string]float64{}
		for _, d := range a {
			r[TextHash(d.Title, d.Description)] = 10
		}
		for _, d := range b {
			r[TextHash(d.Title, d.Description)] = 1
		}
		for _, d := range c {
			r[TextHash(d.Title, d.Description)] = 9.5
		}
		return r
	}
	missing := func(m *Matcher, ds []Doc) (n int) {
		for _, d := range ds {
			if _, ok := m.vs.idx[hashOf(d)]; !ok {
				n++
			}
		}
		return n
	}
	run := func(rank map[string]float64) (*Matcher, string, Report, Report) {
		m, dir := newMatcher(t, newSpy())
		m.vecMax = bound
		m.SetVectorPriority(rank)
		mustWarm(t, m, a...)
		repB := mustWarm(t, m, b...)
		return m, dir, mustWarm(t, m, c...), repB
	}

	m, dir, repC, repB := run(ranked())
	if missing(m, a) != 0 {
		t.Errorf("%d of the high-priority postings lost their vectors to lower-priority ones", missing(m, a))
	}
	if missing(m, b) == 0 || repB.VectorsRefused == 0 {
		t.Errorf("the bound did not bite B: %d missing, report %+v", missing(m, b), repB)
	}
	if missing(m, c) != 0 {
		t.Errorf("%d of the postings that outrank B were not stored", missing(m, c))
	}
	if repC.VectorsRefused != 0 {
		t.Errorf("C outranks B and was refused: %+v", repC)
	}
	if st, err := os.Stat(filepath.Join(dir, vectorLogFileName)); err != nil || st.Size() > bound {
		t.Errorf("log %v (%v) passes the bound %d", st.Size(), err, bound)
	}
	if m.Len() != 23 {
		t.Errorf("%d scores saved, want 23: refusing vectors must not cost scores", m.Len())
	}

	// The same sequence with no ranks is what the old order did: age decides, so
	// the high-priority A goes first.
	old, _, _, _ := run(nil)
	if missing(old, a) == 0 {
		t.Error("fixture: without ranks the oldest (A) should be evicted, so the ranked run proves nothing")
	}
}
