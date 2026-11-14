package semantic

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func numberedDoc(i int) Doc {
	d := k8sDoc()
	d.Key, d.Title = fmt.Sprintf("k%02d", i), fmt.Sprintf("Engineer %d", i)
	return d
}

func docHash(d Doc) string { return TextHash(d.Title, d.Description) }

// A live posting's score must not be evicted to make room while postings
// outside the live set still hold slots: the old oldest-first rule evicted the
// live k00..k03 and kept the dead k07..k11, so every warm re-scored them.
func TestScoreEvictionKeepsLiveSet(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.SetMaxEntries(5)
	live := map[string]float64{}
	for i := range 4 {
		live[docHash(numberedDoc(i))] = 1
	}
	m.SetVectorPriority(live)
	var last Report
	for i := range 12 {
		last = mustWarm(t, m, numberedDoc(i))
	}
	if m.Len() != 5 {
		t.Fatalf("len %d, want 5", m.Len())
	}
	if last.ScoresEvicted != 1 || last.ScoresEvictedLive != 0 {
		t.Errorf("last warm evicted %d (%d live), want 1 (0 live)", last.ScoresEvicted, last.ScoresEvictedLive)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		d := numberedDoc(i)
		_, ok := m2.Lookup(d.Key, docHash(d))
		if want := i < 4 || i == 11; ok != want {
			t.Errorf("%s present=%v want %v (live kept; the newest outside it kept)", d.Key, ok, want)
		}
	}
}

// When the live set alone passes the cap, the lowest-ranked live postings go,
// and the report says the cache is too small rather than hiding it.
func TestScoreEvictionLiveOverflowIsReported(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	m.SetMaxEntries(3)
	live := map[string]float64{}
	var docs []Doc
	for i := range 6 {
		d := numberedDoc(i)
		live[docHash(d)] = float64(i)
		docs = append(docs, d)
	}
	m.SetVectorPriority(live)
	rep := mustWarm(t, m, docs...)
	if rep.ScoresEvicted != 3 || rep.ScoresEvictedLive != 3 {
		t.Errorf("evicted %d (%d live), want 3 (3 live)", rep.ScoresEvicted, rep.ScoresEvictedLive)
	}
	for i, d := range docs {
		_, ok := m.Lookup(d.Key, docHash(d))
		if want := i >= 3; ok != want {
			t.Errorf("%s present=%v want %v (lowest rank goes first)", d.Key, ok, want)
		}
	}
}

// A posting this Warm scored and the live set does not rank counts as the
// highest: a warm must not evict what it just paid to score.
func TestScoreEvictionKeepsThisWarm(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	m.SetMaxEntries(2)
	a, b, c := numberedDoc(0), numberedDoc(1), numberedDoc(2)
	m.SetVectorPriority(map[string]float64{docHash(a): 5, docHash(b): 5})
	mustWarm(t, m, a, b)
	rep := mustWarm(t, m, c)
	if rep.ScoresEvicted != 1 || rep.ScoresEvictedLive != 1 {
		t.Errorf("evicted %d (%d live), want 1 (1 live)", rep.ScoresEvicted, rep.ScoresEvictedLive)
	}
	if _, ok := m.Lookup(c.Key, docHash(c)); !ok {
		t.Error("the posting this warm scored was evicted")
	}
}

func TestEvictOrder(t *testing.T) {
	sf := &scoreFile{Entries: map[string]*entry{
		"dead-old":  {TextHash: "d1", Seq: 1},
		"dead-new":  {TextHash: "d2", Seq: 9},
		"low-old":   {TextHash: "l1", Seq: 2},
		"low-new":   {TextHash: "l2", Seq: 8},
		"high-old":  {TextHash: "h1", Seq: 3},
		"nan":       {TextHash: "n1", Seq: 10},
		"mine":      {TextHash: "m1", Seq: 4},
		"mine-rank": {TextHash: "m2", Seq: 11},
	}}
	keep := keepRule{
		live: map[string]float64{"l1": 1, "l2": 1, "h1": 7, "n1": math.Inf(-1), "m2": 0.5},
		mine: map[string]bool{"m1": true, "m2": true},
	}
	want := []string{"dead-old", "dead-new", "nan", "mine-rank", "low-old", "low-new", "high-old", "mine"}
	for n := 1; n <= len(want); n++ {
		c := &scoreFile{Entries: map[string]*entry{}}
		for k, e := range sf.Entries {
			c.Entries[k] = e
		}
		got, gotLive := c.evict(len(want)-n, keep)
		wantLive := max(0, n-2)
		if got != n || gotLive != wantLive {
			t.Errorf("evict %d: dropped %d (%d live), want %d (%d live)", n, got, gotLive, n, wantLive)
		}
		for i, k := range want {
			if _, ok := c.Entries[k]; ok != (i >= n) {
				t.Errorf("evict %d: %s present=%v", n, k, ok)
			}
		}
	}
	// No live set: age alone decides, as before.
	c := &scoreFile{Entries: map[string]*entry{"a": {TextHash: "x", Seq: 2}, "b": {TextHash: "y", Seq: 1}}}
	if n, live := c.evict(1, keepRule{}); n != 1 || live != 0 || c.Entries["b"] != nil {
		t.Errorf("no live set: dropped %d (%d live), left %v", n, live, c.Entries)
	}
	// Nothing to do, and a negative cap empties.
	if n, _ := c.evict(5, keepRule{}); n != 0 || len(c.Entries) != 1 {
		t.Errorf("under cap evicted %d", n)
	}
	if n, _ := c.evict(-1, keepRule{}); n != 1 || len(c.Entries) != 0 {
		t.Errorf("negative cap evicted %d, left %d", n, len(c.Entries))
	}
}

// Past the byte bound the cache sheds the lowest-ranked entries in proportion to
// the excess, not half of everything, and never a live entry while a dead one
// remains.
func TestSaveScoresByteBoundKeepsLive(t *testing.T) {
	sf := &scoreFile{Version: CacheVersion, ProfileHash: "p", Model: "m", Entries: map[string]*entry{}}
	live := map[string]float64{}
	for i := range 200 {
		th := fmt.Sprintf("%032x", i)
		sf.Entries[fmt.Sprintf("k%03d", i)] = &entry{TextHash: th, Score: 0.5, Seq: uint64(i)}
		if i%2 == 0 {
			live[th] = 1
		}
	}
	whole, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	limit := len(whole) * 9 / 10 // room for about 180 of 200
	path := filepath.Join(t.TempDir(), "scores.json")
	n, nLive, err := saveScores(path, sf, 1000, limit, keepRule{live: live})
	if err != nil {
		t.Fatal(err)
	}
	if nLive != 0 {
		t.Errorf("evicted %d live entries with dead ones left to drop", nLive)
	}
	if n < 20 || n > 40 {
		t.Errorf("evicted %d of 200 for a 10%% overshoot; want a proportional cut, not half", n)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > limit {
		t.Errorf("wrote %d bytes, bound %d", len(raw), limit)
	}
	if _, _, err := saveScores(path, &scoreFile{Entries: map[string]*entry{"k": {TextHash: "x"}}}, 10, 1, keepRule{}); err == nil {
		t.Error("a bound nothing fits under was accepted")
	}
}
