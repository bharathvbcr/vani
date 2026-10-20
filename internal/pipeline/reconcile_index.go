package pipeline

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// recInfo is what reconcile needs from a record: no posting text.
type recInfo struct {
	status posting.Status
	tuple  posting.Key // FallbackKey(company, title, location); "" when it has none
	weak   bool
	// firstSeen ranks records of one tuple (twinOf's cross-ATS survivor).
	firstSeen time.Time
	// urls are the identity links the record is indexed under (urlKeys). They
	// are kept here, not recomputed from the record, so remove deletes exactly
	// what add inserted.
	urls []string
}

// ledgerIndex is the ledger as reconcile reads it at the start of a board:
// every key in order, and the company|title|location tuples it dedupes on.
// It is built from one l.All() and then kept current from the keys each
// board wrote, so a run of B boards reads the ledger once instead of B times
// and hashes each record's tuple once instead of once per board.
type ledgerIndex struct {
	keys []posting.Key // sorted, as l.All() returns them
	info map[posting.Key]recInfo
	ats  map[posting.Key][]posting.Key // tuple -> ATS-keyed records, any status, sorted
	weak map[posting.Key][]posting.Key // tuple -> weakly keyed records not yet terminal, sorted
	// byURL: identity link (identityURL) -> records of any status carrying it, sorted.
	byURL map[string][]posting.Key
	// touched holds the keys written since the index was last refreshed.
	touched map[posting.Key]bool
}

func infoOf(r ledger.Record) recInfo {
	in := recInfo{status: r.Status, weak: weakKey(r.Key), firstSeen: r.FirstSeen, urls: urlKeys(r.Posting)}
	if r.Posting.Company != "" && r.Posting.Title != "" {
		if fk, err := posting.FallbackKey(r.Posting.Company, r.Posting.Title, r.Posting.Location); err == nil {
			in.tuple = fk
		}
	}
	return in
}

func buildIndex(l ledger.Ledger) *ledgerIndex {
	all := l.All()
	x := &ledgerIndex{
		keys: make([]posting.Key, 0, len(all)), info: make(map[posting.Key]recInfo, len(all)),
		ats: map[posting.Key][]posting.Key{}, weak: map[posting.Key][]posting.Key{}, byURL: map[string][]posting.Key{}, touched: map[posting.Key]bool{},
	}
	for _, r := range all { // sorted by key, so each tuple list is built sorted
		x.keys = append(x.keys, r.Key)
		x.add(r.Key, infoOf(r))
	}
	return x
}

func (x *ledgerIndex) add(k posting.Key, in recInfo) {
	x.info[k] = in
	for _, u := range in.urls {
		x.byURL[u] = insertSorted(x.byURL[u], k)
	}
	if in.tuple == "" {
		return
	}
	switch {
	case !in.weak:
		x.ats[in.tuple] = insertSorted(x.ats[in.tuple], k)
	case !in.status.Terminal():
		x.weak[in.tuple] = insertSorted(x.weak[in.tuple], k)
	}
}

func (x *ledgerIndex) remove(k posting.Key) {
	in, ok := x.info[k]
	if !ok {
		return
	}
	for _, u := range in.urls {
		dropKey(x.byURL, u, k)
	}
	if in.tuple == "" {
		return
	}
	dropKey(x.ats, in.tuple, k)
	dropKey(x.weak, in.tuple, k)
}

// dropKey removes k from m[id], deleting the entry when it empties.
func dropKey[K comparable](m map[K][]posting.Key, id K, k posting.Key) {
	ks := m[id]
	if i, found := slices.BinarySearch(ks, k); found {
		ks = slices.Delete(ks, i, i+1)
		if len(ks) == 0 {
			delete(m, id)
		} else {
			m[id] = ks
		}
	}
}

// refresh re-reads every key written since the last refresh, so the index
// again equals a fresh scan of the ledger.
func (x *ledgerIndex) refresh(l ledger.Ledger) {
	for k := range x.touched {
		r, ok := l.Get(k)
		if !ok {
			continue // the write failed and the ledger never had it
		}
		if _, had := x.info[k]; had {
			x.remove(k)
		} else {
			x.keys = insertSorted(x.keys, k)
		}
		x.add(k, infoOf(r))
	}
	clear(x.touched)
}

// atsFor is the ATS-keyed record a weak posting with this tuple repeats: the
// first in key order that is not Dead, as a scan of l.All() finds it. A dead
// record is a job its board no longer lists, so a posting found now is not its
// duplicate: it would be hidden for good.
func (x *ledgerIndex) atsFor(tuple posting.Key) (posting.Key, bool) {
	for _, k := range x.ats[tuple] {
		if x.info[k].status != posting.Dead {
			return k, true
		}
	}
	return "", false
}

// board returns the records under prefix with their status, in key order.
func (x *ledgerIndex) board(prefix string) []posting.Key {
	i, _ := slices.BinarySearch(x.keys, posting.Key(prefix))
	j := i
	for j < len(x.keys) && strings.HasPrefix(string(x.keys[j]), prefix) {
		j++
	}
	return x.keys[i:j]
}

func insertSorted(ks []posting.Key, k posting.Key) []posting.Key {
	i, found := slices.BinarySearch(ks, k)
	if found {
		return ks
	}
	return slices.Insert(ks, i, k)
}

// touching notes every key written through it, so the index can refresh
// exactly those.
type touching struct {
	ledger.Ledger
	touched map[posting.Key]bool
}

func (t touching) Append(ctx context.Context, e ledger.Event) error {
	t.touched[e.Key] = true
	return t.Ledger.Append(ctx, e)
}

// AppendBatch marks every key touched before delegating, so a batch refreshes
// the index exactly as the same events appended one by one would. Embedding's
// promoted AppendBatch would skip the touch and leave the index stale.
func (t touching) AppendBatch(ctx context.Context, evs []ledger.Event) error {
	for _, e := range evs {
		t.touched[e.Key] = true
	}
	return t.Ledger.AppendBatch(ctx, evs)
}

// Reconciler reconciles the boards of one run against one ledger, indexing
// the ledger once rather than once per board. It must be the only writer to
// the ledger between its calls, as runOnce's board loop is (the adapters only
// read it); anything else that writes would leave the index stale.
type Reconciler struct {
	l   ledger.Ledger
	idx *ledgerIndex
	// protected, when set, names records Reconcile must never demote to
	// Duplicate after the fact (the ones the user marked).
	protected func(posting.Key) bool
}

// Protect sets the records Reconcile never demotes to Duplicate once they
// exist: markCrossTwins skips them. A new posting is never protected, having
// no mark yet.
func (r *Reconciler) Protect(f func(posting.Key) bool) { r.protected = f }

// NewReconciler returns a Reconciler over l; it reads the ledger at its first
// Reconcile.
func NewReconciler(l ledger.Ledger) *Reconciler { return &Reconciler{l: l} }

func (r *Reconciler) index() *ledgerIndex {
	if r.idx == nil {
		r.idx = buildIndex(r.l)
	} else {
		r.idx.refresh(r.l)
	}
	return r.idx
}
