package pipeline

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// Annotations of the reading cache. They sit under the triage prefix so a
// changed description (staleAnnotations) and a retriage forget them with the
// reading they describe.
const (
	// AnnTriageModel is the model id a reading was made under. A reading is
	// inherited only from one made under the same id; a ledger record from
	// before this key existed has none and is never inherited from.
	AnnTriageModel = ledger.AnnTriagePrefix + "model"
	// AnnTriageInheritedFrom is the key of the posting whose reading this
	// posting copied instead of asking a backend.
	AnnTriageInheritedFrom = ledger.AnnTriageInheritedFrom
	// AnnTriageSkipped is why the paid triage call was skipped (the posting
	// was kept, unread).
	AnnTriageSkipped = ledger.AnnTriagePrefix + "skipped"
)

// normText lower-cases s and collapses whitespace, so cosmetic differences in
// a title or location do not defeat the cache.
func normText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// readKey identifies what a triage reading depends on: the description (by the
// posting package's own hash, the owner of "same description"), the title, the
// location and the model id. Location is part of the key, so a reading, and a
// drop made on it, is never carried to a posting in another location.
func readKey(p posting.Posting, modelID string) string {
	return strings.Join([]string{p.DescriptionHash(), normText(p.Title), normText(p.Location), modelID}, "\x1f")
}

type readEntry struct {
	done chan struct{}
	ok   bool
	ans  decide.Answer
	from posting.Key
}

// ReadCache lets a posting reuse the triage reading of one that matches it on
// (description hash, title, location, model id). A reading is asked once per
// key even when identical postings advance concurrently: the first asks, the
// others wait for it. Seeded lazily from the ledger, so a reading made in an
// earlier run is reused too.
type ReadCache struct {
	mu      sync.Mutex
	entries map[string]*readEntry
	seeded  bool
}

// NewReadCache returns an empty cache.
func NewReadCache() *ReadCache { return &ReadCache{entries: map[string]*readEntry{}} }

// inheritable reports whether rec holds a real backend reading made under
// modelID that may be copied: not a title read, not an outage, not skipped,
// not unsure.
func inheritable(rec ledger.Record, modelID string) bool {
	a := rec.Annotations
	switch rec.Status {
	case posting.Triaged, posting.Scored, posting.Explained, posting.Digested, posting.Dropped:
	default:
		return false
	}
	backend := a[ledger.AnnTriagePrefix+"backend"]
	return a[AnnTriageModel] == modelID && backend != "" && backend != TitleBackend &&
		a[AnnTriageSkipped] == "" && a[ledger.AnnTriageUnsure] == "" && !Unread(rec)
}

// answerFromAnnotations rebuilds the answer a record's triage annotations
// describe, with the calibration fields DropPolicy reads. Shadow readings are
// left out: they are evidence about the asked posting, never about a copy.
func answerFromAnnotations(a map[string]string) decide.Answer {
	ans := decide.Answer{Backend: a[ledger.AnnTriagePrefix+"backend"], Slots: map[string]decide.SlotReading{}}
	for k, v := range a {
		slot, ok := strings.CutPrefix(k, ledger.AnnTriagePrefix)
		if !ok {
			continue
		}
		slot, ok = strings.CutSuffix(slot, ledger.AnnTriageSuffixReading)
		if !ok || slot == "" || strings.Contains(slot, ".") {
			continue
		}
		pre := ledger.AnnTriagePrefix + slot
		sr := decide.SlotReading{Reading: decide.Reading(v), Value: a[pre]}
		if b := a[pre+ledger.AnnTriageSuffixBackend]; b != "" {
			sr.Detail = "backend=" + b
		}
		if p, err := strconv.ParseFloat(a[pre+ledger.AnnTriageSuffixP], 64); err == nil {
			sr.Jev = &decide.JevCalib{Probability: p}
		}
		if s, err := strconv.ParseFloat(a[pre+ledger.AnnTriageSuffixLappiScore], 64); err == nil {
			sr.Lappi = &decide.LappiCalib{Score: s}
		}
		ans.Slots[slot] = sr
	}
	return ans
}

// seed loads earlier readings from the ledger once; the first record in
// ledger order wins a key. Caller holds c.mu.
func (c *ReadCache) seed(l ledger.Ledger, modelID string) {
	if c.seeded {
		return
	}
	c.seeded = true
	for _, rec := range l.All() {
		if !inheritable(rec, modelID) {
			continue
		}
		k := readKey(rec.Posting, modelID)
		if _, dup := c.entries[k]; dup {
			continue
		}
		done := make(chan struct{})
		close(done)
		c.entries[k] = &readEntry{done: done, ok: true, ans: answerFromAnnotations(rec.Annotations), from: rec.Key}
	}
}

// lookup returns the cached reading for key, or leader true when the caller
// must ask a backend and then report back with finish. A waiter whose leader
// failed becomes the next leader. hit false and leader false means the only
// reading held is the caller's own: ask without registering.
func (c *ReadCache) lookup(ctx context.Context, l ledger.Ledger, self posting.Key, key, modelID string) (ans decide.Answer, from posting.Key, hit, leader bool, err error) {
	for {
		c.mu.Lock()
		c.seed(l, modelID)
		e, ok := c.entries[key]
		if !ok {
			c.entries[key] = &readEntry{done: make(chan struct{})}
			c.mu.Unlock()
			return decide.Answer{}, "", false, true, nil
		}
		c.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return decide.Answer{}, "", false, false, ctx.Err()
		}
		if e.ok {
			if e.from == self {
				return decide.Answer{}, "", false, false, nil
			}
			return e.ans, e.from, true, false, nil
		}
	}
}

// finish records the leader's outcome for key and releases its waiters. A
// failed leader (ok false) removes the entry so a waiter asks next.
func (c *ReadCache) finish(key string, self posting.Key, ans decide.Answer, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		return
	}
	select {
	case <-e.done:
		return // already finished (a seeded entry)
	default:
	}
	if ok {
		ans.Shadow = nil
		e.ok, e.ans, e.from = true, ans, self
	} else {
		delete(c.entries, key)
	}
	close(e.done)
}
