package jobview

import (
	"sync"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// KeptRows keeps each record's static row fields (staticOf: cleaned company
// and title, triage readings, category) between selections of one snapshot,
// for a resident reader (`vani jobs serve`). They read the record alone, no
// marks, clock, config or status, so a snapshot's generation is their whole
// key. The zero value is ready to use and safe for concurrent selections.
type KeptRows struct {
	mu  sync.Mutex
	cur *keptRows
}

// keptRows is one generation's rows. It is never changed once built: a
// selection that holds it keeps reading it while a newer one replaces it.
type keptRows struct {
	gen  uint64
	keys []posting.Key
	rows []rowStatic
}

// RowKeeper is a Ranker that keeps static rows between selections: Kept
// returns the keeper and the generation of the snapshot it ranks, or nil when
// it keeps none (a one-shot command).
type RowKeeper interface {
	Kept() (*KeptRows, uint64)
}

// at is the rows of snap, the snapshot of generation gen, worked out for every
// record on first use.
func (k *KeptRows) at(gen uint64, snap ledger.Snapshot) *keptRows {
	k.mu.Lock()
	defer k.mu.Unlock()
	if c := k.cur; c != nil && c.gen == gen && len(c.keys) == len(snap.Records) {
		return c
	}
	c := &keptRows{gen: gen, keys: make([]posting.Key, len(snap.Records)), rows: make([]rowStatic, len(snap.Records))}
	for i, r := range snap.Records {
		c.keys[i], c.rows[i] = r.Key, staticOf(r)
	}
	k.cur = c
	return c
}

// static is r's static row fields, r being record i of the snapshot: the kept
// row when it is r's, else worked out now.
func (c *keptRows) static(i int, r ledger.Record) rowStatic {
	if c != nil && i >= 0 && i < len(c.keys) && c.keys[i] == r.Key {
		return c.rows[i]
	}
	return staticOf(r)
}

// keptRowsOf is rk's kept rows of snap, or nil when rk keeps none.
func keptRowsOf(rk Ranker, snap ledger.Snapshot) *keptRows {
	if kr, ok := rk.(RowKeeper); ok {
		if k, gen := kr.Kept(); k != nil {
			return k.at(gen, snap)
		}
	}
	return nil
}
