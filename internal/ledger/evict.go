package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"io"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Description eviction.
//
// A File holds one Record per key ever seen, so a ledger that only grows would
// hold every posting body it ever fetched. Dead, Dropped and Duplicate records
// are never read for their text again unless they are reopened, so File keeps
// the rest of such a record (Title, Company, URL, annotations, every other
// Posting field) and drops Posting.Description from memory. The record is
// marked Record.BodyEvicted instead.
//
// The body is not lost: the events file is the source of truth and still holds
// the event that carried it. Each slot remembers where the last Posting-bearing
// line of its key starts and how long it is, plus the length and a hash of the
// body it replaced. Hydrate reads exactly that line back, and accepts it only if
// it decodes, names the same key, carries a posting for that key, and its
// description has the evicted body's length and hash. Anything else (the file
// was truncated, rewritten or edited after Open) is ErrBodyUnavailable, never
// another body.
//
// Semantics, by entry point:
//
//   - Get returns the full record: an evicted body is read back first. If it
//     cannot be (ErrBodyUnavailable), Get returns the record with an empty
//     Description and BodyEvicted still true.
//   - All returns every record as held in memory: an evicted record has an
//     empty Description and BodyEvicted true. It never touches the disk.
//     A caller that reads Description of a record whose status can be Dead,
//     Dropped or Duplicate either skips records with BodyEvicted or calls Full
//     (one Get) for the ones it needs.
//   - ReadSnapshot is unchanged: it is a one-shot read for a short-lived
//     command, holds no long-lived memory, and returns every body.
//   - A record that leaves those statuses without a new posting (reopened) has
//     its body read back as part of the same Append, before anything is
//     written, so a resident record outside the three statuses always has its
//     Description. A reopening that cannot read the body is refused.

// ErrBodyUnavailable means an evicted description could not be read back from
// the events file: the file changed after Open, or the read failed.
var ErrBodyUnavailable = errors.New("ledger: evicted description is unavailable")

// evicts reports whether a record in status s keeps no resident description.
func evicts(s posting.Status) bool {
	switch s {
	case posting.Dead, posting.Dropped, posting.Duplicate:
		return true
	}
	return false
}

// bodySeed keys the body fingerprints. It is per process: the fingerprints live
// only in memory.
var bodySeed = maphash.MakeSeed()

// bodyRef locates the last Posting-bearing event of one key. dlen and sum
// describe the description that event carried; they are set when the body is
// evicted.
type bodyRef struct {
	off  int64 // start of the event line
	n    int32 // line length, newline included (at most MaxLineBytes)
	dlen int32 // length of the evicted description
	sum  uint64
}

// slot is one key's folded record and where its body lives on disk.
type slot struct {
	Record
	ref bodyRef
}

// apply is called with s already folded from e, the event line that starts at
// off and is n bytes long. It records where e's posting lives and evicts or
// restores the description to match the status. A record that leaves the
// evicting statuses without a new posting gets its body from stash (evicted
// earlier in the same commit), then pre (read before the write), then the file
// itself below limit.
func (s *slot) apply(e Event, off int64, n int, stash, pre map[posting.Key]string, rd io.ReaderAt, limit int64) error {
	if e.Posting != nil {
		s.ref = bodyRef{off: off, n: int32(n)}
	}
	switch {
	case evicts(s.Status):
		d := s.Posting.Description
		if d == "" {
			return nil
		}
		s.ref.dlen, s.ref.sum = int32(len(d)), maphash.String(bodySeed, d)
		if stash != nil {
			stash[s.Key] = d
		}
		s.Posting.Description, s.BodyEvicted = "", true
	case s.BodyEvicted:
		d, ok := stash[s.Key]
		if !ok {
			d, ok = pre[s.Key]
		}
		if !ok {
			var err error
			if d, err = s.read(rd, limit); err != nil {
				return err
			}
		}
		s.Posting.Description, s.BodyEvicted = d, false
	}
	return nil
}

// read returns the evicted description, read from rd below limit (the end of
// the intact events). It verifies the line before trusting it.
func (s *slot) read(rd io.ReaderAt, limit int64) (string, error) {
	k, ref := s.Key, s.ref
	if ref.n <= 0 || ref.n > MaxLineBytes || ref.off < 0 || ref.off+int64(ref.n) > limit {
		return "", fmt.Errorf("%w: %s: no intact line for it", ErrBodyUnavailable, k)
	}
	buf := make([]byte, ref.n)
	if _, err := rd.ReadAt(buf, ref.off); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrBodyUnavailable, k, err)
	}
	if buf[len(buf)-1] != '\n' {
		return "", fmt.Errorf("%w: %s: the line moved", ErrBodyUnavailable, k)
	}
	var e Event
	if err := json.Unmarshal(buf[:len(buf)-1], &e); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrBodyUnavailable, k, err)
	}
	if e.Key != k || e.Posting == nil || e.Posting.Key != k {
		return "", fmt.Errorf("%w: %s: the line is another event", ErrBodyUnavailable, k)
	}
	d := e.Posting.Description
	if int32(len(d)) != ref.dlen || maphash.String(bodySeed, d) != ref.sum {
		return "", fmt.Errorf("%w: %s: the line no longer holds the evicted text", ErrBodyUnavailable, k)
	}
	return d, nil
}

// Hydrate returns the record for k with its description, reading an evicted
// body back from the events file. It reports ErrBodyUnavailable when the body
// cannot be verified; it never returns another body in its place.
func (f *File) Hydrate(k posting.Key) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return Record{}, ErrClosed
	}
	s, ok := f.recs[k]
	if !ok {
		return Record{}, fmt.Errorf("ledger: no record %s", k)
	}
	out := cloneRecord(&s.Record)
	if !s.BodyEvicted {
		return out, nil
	}
	d, err := s.read(f.events, f.goodSize)
	if err != nil {
		return out, err
	}
	out.Posting.Description, out.BodyEvicted = d, false
	return out, nil
}

// Full returns r with its description: r itself unless its body is evicted,
// then the record l holds for r.Key. When the body cannot be read back it
// returns r as given, BodyEvicted still true.
func Full(l Ledger, r Record) Record {
	if !r.BodyEvicted {
		return r
	}
	if g, ok := l.Get(r.Key); ok {
		return g
	}
	return r
}

// prepare reads, before anything is written, the body of every evicted record
// that evs reopens without a posting of its own, so a failed read refuses the
// append instead of failing after the events are durable. Caller holds f.mu.
func (f *File) prepare(evs []Event) (map[posting.Key]string, error) {
	var pre map[posting.Key]string
	posted := map[posting.Key]bool{}
	for _, e := range evs {
		if e.Posting != nil {
			posted[e.Key] = true
			continue
		}
		if evicts(e.Status) || posted[e.Key] {
			continue
		}
		s := f.recs[e.Key]
		if s == nil || !s.BodyEvicted {
			continue
		}
		if _, done := pre[e.Key]; done {
			continue
		}
		d, err := s.read(f.events, f.goodSize)
		if err != nil {
			return nil, fmt.Errorf("ledger: reopening %s: %w", e.Key, err)
		}
		if pre == nil {
			pre = map[posting.Key]string{}
		}
		pre[e.Key] = d
	}
	return pre, nil
}

// commit folds evs, written at offs with lens, into memory. Caller holds f.mu
// and has written and synced them. It cannot fail for a reason the events
// could have caught: bodies it needs were read by prepare or are in evs. If the
// file changes underneath it anyway, the record stays evicted and the ledger is
// marked broken, since the file is no longer the one Open replayed.
func (f *File) commit(evs []Event, offs []int64, lens []int, pre map[posting.Key]string) {
	stash := map[posting.Key]string{}
	for i, e := range evs {
		s := slotFor(f.recs, e)
		fold(&s.Record, e, nil)
		if err := s.apply(e, offs[i], lens[i], stash, pre, f.events, f.goodSize); err != nil && f.broken == nil {
			f.broken = fmt.Errorf("%w: %v", errBroken, err)
		}
	}
}
