package pipeline

// The death rule's two promises, checked over random listing sequences against
// an oracle written from the owner's text alone (it reads the ledger only for
// each posting's status, and uses none of liveness.go's functions or constants):
//
//	(a) no posting dies before its applicable hold since its OWN first miss:
//	    20 hours, or 14 days if any listing during its absence episode was a
//	    mass absence, and in both cases not before its second miss;
//	(b) no posting outlives that hold while it stays continuously absent: at the
//	    first full read at or after first miss + hold it is dead.
//
// A mass absence is a full listing that leaves at least a quarter of the
// board's open postings (distinct keys, as the ledger held them before the
// listing) absent with at least 10 open, or shows none of at least 2 open.

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	fuzzSequences = 250 // VANI_FUZZ_SEQUENCES=2000 is the full run (about 8 minutes under load)
	fuzzReads     = 30
)

// fuzzGaps are the spacings between reads: hourly retries, daily runs, a
// laptop asleep for days, a board left unread for two weeks.
var fuzzGaps = []time.Duration{
	time.Hour, 3 * time.Hour, 12 * time.Hour, 19 * time.Hour, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour,
	30 * time.Hour, 48 * time.Hour, 5 * 24 * time.Hour, 15 * 24 * time.Hour,
}

type fuzzRead struct {
	at      time.Time
	listed  []int
	partial bool
}

// fuzzSequence builds one board's reads from rng: a universe of ids, each read a
// healthy listing, a few missing, a mass drop, an empty answer, a renumbered
// board, repeated rows or a partial listing.
func fuzzSequence(rng *rand.Rand) []fuzzRead {
	universe := 2 + rng.IntN(29)
	next := universe // ids a renumbered board brings
	at := t0
	cur := make([]int, universe)
	for i := range cur {
		cur[i] = i
	}
	out := make([]fuzzRead, 0, fuzzReads)
	for range fuzzReads {
		var ids []int
		partial := false
		switch m := rng.IntN(20); {
		case m < 9: // healthy, a role or two closes
			if len(cur) > 0 && rng.IntN(3) == 0 {
				i := rng.IntN(len(cur))
				cur = append(cur[:i:i], cur[i+1:]...)
			}
			ids = append(ids, cur...)
		case m < 11: // a few missing this read only
			for _, id := range cur {
				if rng.IntN(10) != 0 {
					ids = append(ids, id)
				}
			}
		case m < 13: // a mass drop that may or may not recover
			keep := rng.IntN(len(cur) + 1)
			ids = append(ids, cur[:keep]...)
		case m < 14: // empty answer
		case m < 15: // renumbered: the old ids never come back
			cur = cur[:0]
			for range universe {
				cur = append(cur, next)
				next++
			}
			ids = append(ids, cur...)
		case m < 16: // one row repeated
			if len(cur) > 0 {
				id := cur[rng.IntN(len(cur))]
				for range 1 + rng.IntN(20) {
					ids = append(ids, id)
				}
			}
		case m < 17: // a partial listing: ages nothing
			partial = true
			for _, id := range cur {
				if rng.IntN(2) == 0 {
					ids = append(ids, id)
				}
			}
		default: // the board is back at full strength
			ids = append(ids, cur...)
		}
		rng.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
		at = at.Add(fuzzGaps[rng.IntN(len(fuzzGaps))])
		out = append(out, fuzzRead{at: at, listed: ids, partial: partial})
	}
	return out
}

// fuzzState is the oracle's memory of one posting's absence episode.
type fuzzState struct {
	misses int
	first  time.Time
	massed bool
}

func TestDeathRuleBoundsFuzz(t *testing.T) {
	n := fuzzSequences
	if testing.Short() {
		n = 60
	}
	if v, err := strconv.Atoi(os.Getenv("VANI_FUZZ_SEQUENCES")); err == nil && v > 0 {
		n = min(v, 20000) // 2000 is the full run
	}
	base := t.TempDir()
	var deaths, held, massReads atomic.Int64
	const shards = 8
	t.Run("shards", func(t *testing.T) {
		for sh := 1; sh <= shards; sh++ {
			t.Run(fmt.Sprint(sh), func(t *testing.T) {
				t.Parallel()
				d, h, m := fuzzSeeds(t, base, sh, n, shards)
				deaths.Add(d)
				held.Add(h)
				massReads.Add(m)
			})
		}
	})
	if t.Failed() {
		return
	}
	// The sequences reached what they are for.
	if deaths.Load() == 0 || held.Load() == 0 || massReads.Load() == 0 {
		t.Fatalf("sequences did not reach the rule: %d deaths, %d held reads, %d mass reads", deaths.Load(), held.Load(), massReads.Load())
	}
	t.Logf("%d sequences x %d reads: %d deaths, %d held postings-reads, %d mass reads", n, fuzzReads, deaths.Load(), held.Load(), massReads.Load())
}

// fuzzSeeds runs the sequences first, first+step, ... up to n.
func fuzzSeeds(t *testing.T, base string, first, n, step int) (deaths, held, massReads int64) {
	board := probeBoard("fuzz")
	for seed := first; seed <= n; seed += step {
		reads := fuzzSequence(rand.New(rand.NewPCG(uint64(seed), 99)))
		l, err := ledger.Open(filepath.Join(base, fmt.Sprint(seed)))
		if err != nil {
			t.Fatal(err)
		}
		episode := map[posting.Key]*fuzzState{}
		failf := func(i int, format string, a ...any) {
			l.Close()
			t.Fatalf("seed %d read %d: %s", seed, i, fmt.Sprintf(format, a...))
		}
		for i, r := range reads {
			var listed []posting.Posting
			distinct := map[posting.Key]bool{}
			for _, id := range r.listed {
				listed = append(listed, probePost(t, board, id))
				distinct[probeKey(t, board, id)] = true
			}
			// The ledger as it stood before the listing, as the owner's rule reads it.
			var open []posting.Key
			for _, rec := range l.All() {
				if rec.Status != posting.Dead && rec.Status != posting.Duplicate && rec.Status != posting.Dropped {
					open = append(open, rec.Key)
				}
			}
			rc := NewReconciler(l)
			var err error
			if r.partial {
				_, err = rc.ReconcilePartial(ctx, board, listed, r.at)
			} else {
				_, err = rc.Reconcile(ctx, board, listed, r.at)
			}
			if err != nil {
				failf(i, "reconcile: %v", err)
			}
			if r.partial { // ages nothing, but a posting it shows has reappeared
				for k := range distinct {
					delete(episode, k)
				}
				continue
			}
			absent := 0
			for _, k := range open {
				if !distinct[k] {
					absent++
				}
			}
			mass := (len(open) >= 2 && absent == len(open)) || (len(open) >= 10 && 4*absent >= len(open))
			if mass {
				massReads++
			}
			for k := range distinct { // a listed posting's episode ends
				delete(episode, k)
			}
			for _, k := range open {
				if distinct[k] {
					continue
				}
				st := episode[k]
				if st == nil {
					st = &fuzzState{first: r.at}
					episode[k] = st
				}
				st.misses++
				st.massed = st.massed || mass
				hold := specOrdinaryHold
				if st.massed {
					hold = specMassHold
				}
				rec, _ := l.Get(k)
				overdue := st.misses >= 2 && !r.at.Before(st.first.Add(hold))
				switch {
				case rec.Status == posting.Dead && !overdue:
					failf(i, "%s died with %d misses, %s after its first miss, hold %s (massed %v)", k, st.misses, r.at.Sub(st.first), hold, st.massed)
				case rec.Status != posting.Dead && overdue:
					failf(i, "%s alive with %d misses, %s after its first miss, hold %s (massed %v)", k, st.misses, r.at.Sub(st.first), hold, st.massed)
				case rec.Status == posting.Dead:
					deaths++
					delete(episode, k)
				case st.misses >= 2:
					held++
				}
			}
			for _, k := range open { // everything the listing showed is open or revived
				if distinct[k] {
					if rec, _ := l.Get(k); rec.Status == posting.Dead {
						failf(i, "%s was listed and is dead", k)
					}
				}
			}
		}
		l.Close()
	}
	return deaths, held, massReads
}
