package main

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// `vani jobs serve` keeps each posting's Blend (jobinsight.RankContext.BlendFor:
// the clock- and marks-free stage of its score) between requests, so a request
// closes each score (freshness, the company's marks) instead of building it.
//
// A kept Blend is reused only when everything it read is what this request
// resolved: the key (blendKey) names the records' generation and every
// optional part of the rank context, each as built, so a store that could not
// be read keys apart from one that was read. Per posting, an entry also holds
// what can change under one key: the posting's status as this read sees it,
// whether its stored facts were there, and its company's blurb. A posting held
// without its text and without stored facts is never kept: its derivers note
// it missing (textFacts.noteMissing), and a kept Blend would skip that.

// blendKey is everything a posting's Blend reads besides the posting, as one
// request resolved it.
type blendKey struct {
	gen      uint64
	cfgOK    bool
	weights  string  // the weights in use, "" for the hand weights
	rules    uint64  // drop rules and rank penalties, hashed: their values can name the owner's work authorization
	escalate float64 // decide.escalate.below_p as the ranking reads it
	titles   string  // titleSignalsKey, "" when there are none
	target   string  // the target summary, "" when keywords are not marked against it
	vocab    string  // the vocabulary's fingerprint
	sem      *semantic.Matcher
	kb       string // kbmatch identity (kbIdentity), "" when off
	manual   uint64 // manual readings' content (manualFingerprint), 0 when none
}

// keptBlend is one posting's Blend and what it was computed under.
type keptBlend struct {
	key    posting.Key
	status posting.Status
	fact   bool
	boiler uint64
	blend  jobinsight.Blend
}

// blendStore is the Blends kept under one key, one slot per record of the
// generation's snapshot. Entries are written once and never changed.
type blendStore struct {
	key     blendKey
	entries []atomic.Pointer[keptBlend]
}

// blendKeep holds the current blendStore. The zero value is ready.
type blendKeep struct {
	mu  sync.Mutex
	cur *blendStore
	// lookups and misses count Blends asked for and computed, for tests.
	lookups, misses atomic.Int64
}

// store is the blendStore for key over n records: the current one when it was
// made under key, else a new empty one that replaces it. A request keeps the
// store it got, so a later request under another key does not change it.
func (k *blendKeep) store(key blendKey, n int) *blendStore {
	k.mu.Lock()
	defer k.mu.Unlock()
	if c := k.cur; c != nil && c.key == key && len(c.entries) == n {
		return c
	}
	k.cur = &blendStore{key: key, entries: make([]atomic.Pointer[keptBlend], n)}
	return k.cur
}

// order is r's Result without the factor list (RankContext.OrderOf), its
// Blend kept between requests when in keeps them. i is r's index in the
// snapshot, -1 when not known.
func (in *insight) order(i int, r ledger.Record) jobinsight.Result {
	if in.blends == nil || i < 0 || i >= len(in.blends.entries) {
		return in.rc.OrderOf(r)
	}
	return in.rc.CloseOrder(in.blendFor(i, r), r)
}

// blendFor is the Blend of r, record i, from in.blends when an entry there was
// computed under what this read sees, else computed now and kept.
func (in *insight) blendFor(i int, r ledger.Record) *jobinsight.Blend {
	keep := &in.res.blends
	keep.lookups.Add(1)
	_, fact := in.facts.get(r.Key)
	// A posting held without its text and without facts is never read from
	// the keep or put in it: its derivers must run, to note it missing.
	lean := !fact && r.BodyEvicted
	boiler := in.boilerPrint(r.Posting.Company)
	slot := &in.blends.entries[i]
	if e := slot.Load(); !lean && e != nil && e.key == r.Key && e.status == r.Status && e.fact == fact && e.boiler == boiler {
		return &e.blend
	}
	keep.misses.Add(1)
	e := &keptBlend{key: r.Key, status: r.Status, fact: fact, boiler: boiler, blend: in.rc.BlendFor(r)}
	if !lean {
		slot.Store(e)
	}
	return &e.blend
}

// boilerPrint is a fingerprint of company's blurb terms (in.boiler), 0 for
// none. It is worked out once per company per request.
func (in *insight) boilerPrint(company string) uint64 {
	ck := companyKey(company)
	if fp, ok := in.boilerFPs[ck]; ok {
		return fp
	}
	var fp uint64
	var names []string
	for t, on := range in.boiler[ck] {
		if on { // jobinsight.Without drops only the terms set true
			names = append(names, t)
		}
	}
	if len(names) > 0 {
		slices.Sort(names)
		h := fnv.New64a()
		for _, t := range names {
			fmt.Fprintf(h, "%q;", t)
		}
		fp = h.Sum64() | 1 // never 0, which is "no blurb"
	}
	if in.boilerFPs == nil {
		in.boilerFPs = map[string]uint64{}
	}
	in.boilerFPs[ck] = fp
	return fp
}

// keepBlends sets in.blends from the resident reader's keep under what in's
// rank context resolved. kb is the kbmatch identity, ok false when the store
// changed while it was read (nothing is then kept for this request).
func (in *insight) keepBlends(cfg config.Config, cfgOK bool, kb string, kbOK bool, nRecords int) {
	if in.res == nil || !kbOK {
		return
	}
	key := blendKey{gen: in.gen, cfgOK: cfgOK, escalate: in.rc.EscalateBelowP, vocab: in.vocab.Fingerprint(), kb: kb,
		manual: manualFingerprint(in.manual)}
	if in.rc.Weights != nil {
		key.weights = fmt.Sprintf("%#v", *in.rc.Weights)
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "%#v", in.rc.DropRules)
	key.rules = h.Sum64()
	if in.rc.TitleRank != nil {
		key.titles = titleSignalsKey(cfg.TitleSignals)
	}
	if in.rc.Target && in.target != nil {
		key.target = cfg.TargetSummary
	}
	if in.rc.Semantic != nil {
		key.sem = in.sem
	}
	in.blends = in.res.blends.store(key, nRecords)
}

// kbStat is the kbmatch store's file as found: size and modification time,
// missing, or why it could not be looked at. newInsightWith reads it before and
// after building the lookup, and keeps nothing when the two differ.
func kbStat(stateDir string) string {
	st, err := os.Stat(filepath.Join(stateDir, kbmatch.FileName))
	switch {
	case err == nil:
		return fmt.Sprintf("%d %d", st.Size(), st.ModTime().UnixNano())
	case errors.Is(err, fs.ErrNotExist):
		return "missing"
	default:
		return "error " + err.Error()
	}
}

// manualFingerprint is a fingerprint of the manual readings' content, 0 when
// there are none.
func manualFingerprint(s *manualReadingSet) uint64 {
	if s == nil || len(s.m) == 0 {
		return 0
	}
	keys := make([]posting.Key, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := fnv.New64a()
	for _, k := range keys {
		fmt.Fprintf(h, "%q %#v;", k, s.m[k])
	}
	return h.Sum64() | 1
}

// Kept is jobview.RowKeeper: the resident reader's static rows, for the
// generation this insight's snapshot was read at.
func (in *insight) Kept() (*jobview.KeptRows, uint64) {
	if in.res == nil {
		return nil, 0
	}
	return &in.res.rows, in.gen
}
