package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A pending reopen is a dropped posting the current rules would let back,
// kept dropped until its board lists it again. A dropped posting is closed, so
// no listing ages it and the ledger holds no sign that its board still lists
// it: reopening it at once would put a long-closed role through scoring, the
// paid notes call and the digest before two missed listings killed it. Waiting
// for the board's next full listing to show it costs nothing for a role that
// is gone, and the reopen is judged again then, on the listed text.
//
// The set is a state file beside the ledger, not ledger annotations: an event
// on a dropped record moves its last event, and the digest footer counts a
// drop on the day of that event (cmd/vani countsFor), so marking thousands of
// drops would report them all as dropped today. The ledger lock serialises the
// file's writers: the retriage command and the run's listing both hold it.

const (
	// PendingReopenFile is where the set is kept, under the state dir.
	PendingReopenFile = "reopen/pending.json"
	// PendingReopenExpiry is how long a pending reopen waits for its board to
	// list it. Once the board's clean full listings (not short, no skipped rows) have left it out for this
	// much read time it is cleared and counted: absence from two weeks of daily
	// listings is far past the two misses and 20 hours a live posting dies on,
	// and long enough to ride out a board that fails or is rate limited for days
	// (a failed listing counts nothing; a gap between reads counts at most
	// ClockWindow; see SightingReopen.expire).
	PendingReopenExpiry = 14 * 24 * time.Hour
	// MaxPendingExclusions bounds the exclusion-class entries within
	// MaxPendingReopens. A loosened broad rule can free tens of thousands of
	// drops at once (the live ledger holds about 34,000); without its own
	// bound they could fill the set and make every title and model mark OverCap.
	// The rest of the bound is always left to those.
	MaxPendingExclusions = 30_000
	// MaxPendingReopens bounds the set. The live ledger holds about 34,000
	// drops, and a mark beyond the bound is counted, never silently lost.
	MaxPendingReopens = 40_000
	// MaxRefreshUnjudged bounds how many unjudged drops one retriage sends for
	// a fresh read (ReopenScope.RefreshUnjudged): each costs a decision-model
	// call when it comes back.
	MaxRefreshUnjudged = 500
	// maxPendingReopenBytes bounds the file read.
	maxPendingReopenBytes = 16 << 20
	pendingReopenVersion  = 1
)

// The classes of a pending reopen: what the drop was, and so how it comes
// back at the sighting.
const (
	reopenClassTitle   = "title"   // a title drop: back to Live
	reopenClassModel   = "model"   // a model drop on a reading still bound to its text: back on that reading
	reopenClassFresh   = "fresh"   // a model drop whose text moved since its reading: back to Live
	reopenClassRefresh = "refresh" // an unjudged model drop the owner asked to re-read: back to Live
	// reopenClassExclusion is an exclusion drop the exclusions no longer match
	// (ApplyExclusions), or one a listing shows moved (SightingReopen.event): at
	// the sighting it is judged against the exclusions on the listed text and
	// comes back to Live, or stays dropped.
	reopenClassExclusion = "exclusion"
)

func reopenClassOf(c dropClass) string {
	switch c {
	case dropTitle:
		return reopenClassTitle
	case dropModel:
		return reopenClassModel
	case dropFresh:
		return reopenClassFresh
	case dropRefresh:
		return reopenClassRefresh
	}
	return ""
}

func validReopenClass(s string) bool {
	switch s {
	case reopenClassTitle, reopenClassModel, reopenClassFresh, reopenClassRefresh, reopenClassExclusion:
		return true
	}
	return false
}

// PendingReopen is one posting's pending reopen.
type PendingReopen struct {
	Class  string    `json:"class"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
	// Out is how much time its board was read (in a full or short listing)
	// without it, and Last the read that last counted: the entry expires once
	// Out reaches PendingReopenExpiry. Time the board was not read, or a clock
	// that jumped, is not counted (SightingReopen.expire).
	Out  time.Duration `json:"out,omitempty"`
	Last time.Time     `json:"last,omitzero"`
}

// PendingReopens is the set, by posting key.
type PendingReopens struct {
	Items map[posting.Key]PendingReopen
	// Gone are the exclusion drops whose pending reopen expired unseen: their
	// board was read without them for PendingReopenExpiry, so they are probably
	// closed for good. The exclusion pass does not mark them again until a full
	// listing shows them (SightingReopen.sighted), so the expiry is not undone
	// every two weeks. The value is when the entry expired. Counted, never lost
	// silently: GoneLen.
	Gone map[posting.Key]time.Time
	// Expired counts entries LoadPendingReopens cleared for age.
	Expired int
}

// NewPendingReopens returns an empty set.
func NewPendingReopens() *PendingReopens {
	return &PendingReopens{Items: map[posting.Key]PendingReopen{}, Gone: map[posting.Key]time.Time{}}
}

// Len is how many reopens are pending.
func (s *PendingReopens) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Items)
}

// ClearUnwatched clears the entries (and expired marks) of boards watched says
// are no longer on the watchlist: no listing will ever show them, so they would
// wait for ever. It counts them in Expired and returns how many it cleared.
// Clock-free, unlike the age limit it replaces.
func (s *PendingReopens) ClearUnwatched(watched func(posting.Key) bool) int {
	n := 0
	for k := range s.Items {
		if !watched(k) {
			delete(s.Items, k)
			n++
		}
	}
	for k := range s.Gone {
		if !watched(k) {
			delete(s.Gone, k)
		}
	}
	s.Expired += n
	return n
}

// GoneLen is how many exclusion drops expired unseen and are not marked again
// until listed.
func (s *PendingReopens) GoneLen() int {
	if s == nil {
		return 0
	}
	return len(s.Gone)
}

// count is how many pending reopens are of class.
func (s *PendingReopens) count(class string) int {
	n := 0
	for _, e := range s.Items {
		if e.Class == class {
			n++
		}
	}
	return n
}

type pendingReopenFile struct {
	V       int                           `json:"v"`
	Updated time.Time                     `json:"updated"`
	Items   map[posting.Key]PendingReopen `json:"items"`
	Gone    map[posting.Key]time.Time     `json:"gone,omitempty"`
}

// LoadPendingReopens reads the set at path. A missing file is an empty set.
// A file that is oversized, malformed, of another version or holds an entry
// it cannot read is an error, so a set that could not be read is never taken
// for an empty one. Nothing is cleared for age here: a clock cannot be trusted
// to say an entry is old (one 28 days ahead used to wipe the whole set), so
// expiry is counted in read time (SightingReopen.expire) and a board that left
// the watchlist is cleared by ClearUnwatched. now is not used.
func LoadPendingReopens(path string, _ time.Time) (*PendingReopens, error) {
	f, err := atomicfile.OpenRead(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewPendingReopens(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("pending reopens: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPendingReopenBytes+1))
	if err != nil {
		return nil, fmt.Errorf("pending reopens: read: %w", err)
	}
	if len(raw) > maxPendingReopenBytes {
		return nil, fmt.Errorf("pending reopens: %s is over %d bytes", path, maxPendingReopenBytes)
	}
	var pf pendingReopenFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pf); err != nil {
		return nil, fmt.Errorf("pending reopens: %s: %w", path, err)
	}
	if pf.V != pendingReopenVersion {
		return nil, fmt.Errorf("pending reopens: %s: version %d, want %d", path, pf.V, pendingReopenVersion)
	}
	if len(pf.Items) > MaxPendingReopens {
		return nil, fmt.Errorf("pending reopens: %s holds %d entries, over %d", path, len(pf.Items), MaxPendingReopens)
	}
	s := NewPendingReopens()
	for k, e := range pf.Items {
		if k.ATS() == "" || !validReopenClass(e.Class) || e.At.IsZero() {
			return nil, fmt.Errorf("pending reopens: %s: entry %q is not readable", path, k)
		}
		s.Items[k] = e
	}
	if len(pf.Gone) > MaxPendingReopens {
		return nil, fmt.Errorf("pending reopens: %s holds %d expired entries, over %d", path, len(pf.Gone), MaxPendingReopens)
	}
	for k, at := range pf.Gone {
		if k.ATS() == "" || at.IsZero() {
			return nil, fmt.Errorf("pending reopens: %s: expired entry %q is not readable", path, k)
		}
		s.Gone[k] = at
	}
	return s, nil
}

// Save writes the set to path atomically.
func (s *PendingReopens) Save(path string, now time.Time) error {
	if len(s.Items) > MaxPendingReopens || len(s.Gone) > MaxPendingReopens {
		return fmt.Errorf("pending reopens: %d entries and %d expired, over %d", len(s.Items), len(s.Gone), MaxPendingReopens)
	}
	for k, e := range s.Items {
		if k.ATS() == "" || !validReopenClass(e.Class) || e.At.IsZero() {
			return fmt.Errorf("pending reopens: entry %q is not writable", k)
		}
	}
	b, err := json.MarshalIndent(pendingReopenFile{V: pendingReopenVersion, Updated: now.UTC(), Items: s.Items, Gone: s.Gone}, "", " ")
	if err != nil {
		return fmt.Errorf("pending reopens: encode: %w", err)
	}
	if len(b) > maxPendingReopenBytes {
		return fmt.Errorf("pending reopens: encodes to %d bytes, over %d", len(b), maxPendingReopenBytes)
	}
	if err := atomicfile.Write(path, b); err != nil {
		return fmt.Errorf("pending reopens: %w", err)
	}
	return nil
}

// SightingReopen applies pending reopens as a run's full listings show their
// postings again (Reconciler.ReopenOnSighting). Each is judged again under
// Policy and Scope, on the listed text, exactly as a reopen at the time of
// marking would have been: what the rules now drop stays dropped and its
// pending reopen is cleared. It counts what it did.
type SightingReopen struct {
	Policy  DropPolicy
	Scope   ReopenScope
	Pending *PendingReopens
	// Applied reopened at a sighting; Cleared were judged again and stay
	// dropped; Expired were left out of their board's full listings for
	// PendingReopenExpiry.
	Applied, Cleared, Expired int
	changed                   bool
}

// Changed reports whether the set changed and needs saving.
func (s *SightingReopen) Changed() bool { return s != nil && s.changed }

func (s *SightingReopen) pending(k posting.Key) (PendingReopen, bool) {
	if s == nil || s.Pending == nil {
		return PendingReopen{}, false
	}
	e, ok := s.Pending.Items[k]
	return e, ok
}

func (s *SightingReopen) forget(k posting.Key) {
	if s.Pending == nil {
		return
	}
	delete(s.Pending.Items, k)
	s.changed = true
}

// movedExclusion reports whether rec is an exclusion drop that a full listing
// shows moved: a field the exclusions key on (title, company, location, remote)
// differs between what is stored and what the board lists now, with the
// description unchanged (a changed description reopens the record whatever the
// rules say, in Reconcile). The drop was judged on the stored fields, so the
// listing's may no longer match it. A nil SightingReopen has no policy to judge
// with, so no record is one.
func (s *SightingReopen) movedExclusion(rec ledger.Record, listed posting.Posting) bool {
	if s == nil || !Excluded(rec) {
		return false
	}
	st := rec.Posting
	return st.Title != listed.Title || st.Company != listed.Company || st.Location != listed.Location || st.Remote != listed.Remote
}

// event is what a full listing that shows rec (dropped, with a pending
// reopen or an exclusion drop whose fields moved) again writes: the reopen,
// judged again on listed, or nothing. A pending reopen is spent either way.
func (s *SightingReopen) event(l ledger.Ledger, rec ledger.Record, listed posting.Posting, hash string, now time.Time) (ledger.Event, bool) {
	e, queued := s.pending(rec.Key)
	if queued {
		s.forget(rec.Key)
	} else {
		e.Class = reopenClassExclusion // only a moved exclusion drop gets here unqueued
	}
	if e.Class == reopenClassExclusion {
		return s.exclusionEvent(rec, listed, hash, now, queued)
	}
	r := rec
	r.Posting, r.BodyEvicted = listed, false
	sc := s.Scope
	sc.Defer, sc.Pending = nil, nil
	sc.Watched = func(posting.Key) bool { return true } // its board just listed it
	sc.TitleOnly = e.Class == reopenClassTitle
	sc.RefreshUnjudged = e.Class == reopenClassRefresh
	var ev ledger.Event
	switch c := planPolicyDrop(l, r, s.Policy, sc); c {
	case dropTitle:
		ev = reopenEvent(r, now, reasonTitleDrop+", listed again")
	case dropFresh:
		ev = reopenEvent(r, now, reasonFreshDrop+", listed again")
	case dropRefresh:
		ev = reopenEvent(r, now, reasonRefreshDrop+", listed again")
	case dropModel:
		ev = keepReadingEvent(r, s.Policy, now)
		ev.Reason += ", listed again"
	default:
		s.Cleared++
		return ledger.Event{}, false
	}
	// The listing is the posting's text now: the event carries it, so the
	// record takes it back with its body and a clean miss count.
	ev.Posting = &listed
	if ev.Annotations == nil {
		ev.Annotations = map[string]string{}
	}
	ev.Annotations[DescHashKey] = hash
	s.Applied++
	return ev, true
}

const reasonExclusionDrop = "the exclusions no longer match it"

// exclusionEvent judges an exclusion drop again on the listed text: back to
// Live (triage forgotten, the listing taken) when the exclusions no longer
// match it. When they still match, a drop whose fields moved stays dropped but
// takes the listing and the reason the exclusions give it now (an event that
// keeps Status Dropped, so the exclusion pass judges the board's text from here
// on, not the stale fields it would otherwise mark pending again every run);
// one whose fields did not move gets no event. Only a record that is still an
// exclusion drop is judged; a queued entry on any other record is just spent.
// queued says the drop had a pending reopen, which Cleared counts when the
// drop stays.
func (s *SightingReopen) exclusionEvent(rec ledger.Record, listed posting.Posting, hash string, now time.Time, queued bool) (ledger.Event, bool) {
	why, still := s.Policy.excluded(listed)
	if still && Excluded(rec) && s.movedExclusion(rec, listed) {
		if queued {
			s.Cleared++
		}
		return ledger.Event{V: ledger.SchemaVersion, At: now, Key: rec.Key, Status: posting.Dropped, Posting: &listed,
			Annotations: map[string]string{AnnExcluded: why}, Reason: "listed again with moved fields; the exclusions still match it"}, true
	}
	if still || !Excluded(rec) {
		if queued {
			s.Cleared++
		}
		return ledger.Event{}, false
	}
	ev := reopenEvent(rec, now, reasonExclusionDrop+", listed again")
	ev.Posting = &listed
	ev.Annotations[DescHashKey] = hash
	s.Applied++
	return ev, true
}

// sighted records that a full listing showed k and reports whether k was Gone:
// an exclusion drop that had expired unseen is back, so it is judged against
// the exclusions on the listed text as a pending reopen would be, and is no
// longer remembered as gone.
func (s *SightingReopen) sighted(k posting.Key) bool {
	if s == nil || s.Pending == nil {
		return false
	}
	if _, ok := s.Pending.Gone[k]; !ok {
		return false
	}
	delete(s.Pending.Gone, k)
	s.changed = true
	return true
}

// expire clears the pending reopens of prefix's board that its full or short
// listings left out (not in seen) for PendingReopenExpiry, and those whose
// record is no longer dropped (a changed description already reopened it).
//
// Only a clean full listing counts (short: a listing short of its count or one
// that skipped unreadable rows counts nothing, since the posting may be behind
// the rows it did not read or list).
//
// The time counted is read time, not calendar time since the mark: each read
// that leaves the entry out adds the time since the entry's last counted read,
// at most ClockWindow (a board that failed for a week then answers once adds
// three days, not seven), and nothing when the clock is behind that read or
// when the read froze for events ahead of its clock (frozen: the clock the
// death rule distrusts is not used to expire a posting either). A clock that
// jumped ahead therefore adds at most ClockWindow, never the jump.
//
// An expired exclusion drop is remembered in Gone, so the next exclusion pass
// does not mark it again until a listing shows it.
func (s *SightingReopen) expire(l ledger.Ledger, prefix string, seen map[posting.Key]bool, now time.Time, frozen, short bool) {
	if s == nil || s.Pending == nil || prefix == "" {
		return
	}
	for k, e := range s.Pending.Items {
		if !strings.HasPrefix(string(k), prefix) {
			continue
		}
		if r, ok := l.Get(k); !ok || r.Status != posting.Dropped {
			s.forget(k)
			continue
		}
		if seen[k] || frozen {
			continue
		}
		if short {
			// A listing short of its count, or one that skipped rows it could not
			// read, cannot say the posting is gone: it counts nothing, and the
			// time it spans is not counted by the next clean listing either.
			if !e.Last.IsZero() && e.Last.Before(now) {
				e.Last = now
				s.Pending.Items[k] = e
				s.changed = true
			}
			continue
		}
		if e.Last.IsZero() || e.Last.After(now) {
			// The first read that leaves it out, or one whose clock is behind the
			// last counted read (that read ran ahead, or this one is set back):
			// count nothing, and count from this read on, so one jump never
			// stalls the entry until real time passes it.
			e.Last = now
			s.Pending.Items[k] = e
			s.changed = true
			continue
		}
		e.Out += min(now.Sub(e.Last), ClockWindow)
		e.Last = now
		if e.Out < PendingReopenExpiry {
			s.Pending.Items[k] = e
			s.changed = true
			continue
		}
		s.forget(k)
		s.Expired++
		if e.Class == reopenClassExclusion && len(s.Pending.Gone) < MaxPendingReopens {
			if s.Pending.Gone == nil {
				s.Pending.Gone = map[posting.Key]time.Time{}
			}
			s.Pending.Gone[k] = now.UTC()
		}
	}
}
