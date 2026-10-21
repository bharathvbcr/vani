package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/maphash"
	"io"
	"os"
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// Scoped reads.
//
// A command that enriches one row needs the full text of that row's company
// and only the small fields (key, company, URLs, annotations, timestamps) of
// every other posting. Decoding every description is most of the cost of
// ReadSnapshot at 10k postings (about 60 MB of JSON), so ReadSnapshotScoped
// folds the ledger without materialising any description, lets the caller say
// which records it needs whole, and reads exactly those lines back.
//
// A record whose body was not loaded is marked BodyEvicted with an empty
// Description, the same convention evict.go uses for dead records: a consumer
// that reads Description checks BodyEvicted first. Every other field of such a
// record is complete.

// postingKey and descriptionKey are the JSON names stripDescription looks for.
const (
	postingKey     = "posting"
	descriptionKey = "description"
)

// stripDescription finds the description string of the event's posting object
// in line (an event as Marshal writes it) and returns line with that string's
// contents cut out (built in buf), and how many bytes were cut. ok is false
// when the line does not have the expected shape (no posting, no description,
// whitespace between a key and its colon, an escaped key name): the caller then
// decodes the line whole, so an unusual line is never misread, only not
// shortened. The cut is found by a structural scan that jumps from quote to
// quote, so the description itself is never tokenised or copied; its escapes
// are not validated (they are when a body is read back).
func stripDescription(line, buf []byte) (lean []byte, cut int, ok bool) {
	depth, inPosting := 0, false
	for i := 0; i < len(line); {
		switch line[i] {
		case '{', '[':
			depth++
			i++
		case '}', ']':
			depth--
			i++
		case '"':
			end := stringEnd(line, i)
			if end < 0 {
				return nil, 0, false
			}
			name := line[i+1 : end] // raw contents; escapes are left as written
			i = end + 1
			if i >= len(line) || line[i] != ':' {
				continue // a value, not a key
			}
			i++
			switch {
			case depth == 1:
				inPosting = string(name) == postingKey
			case depth == 2 && inPosting && string(name) == descriptionKey:
				if i >= len(line) || line[i] != '"' {
					return nil, 0, false
				}
				vend := stringEnd(line, i)
				if vend < 0 {
					return nil, 0, false
				}
				buf = append(buf[:0], line[:i+1]...)
				buf = append(buf, line[vend:]...)
				return buf, vend - i - 1, true
			}
		default:
			i++
		}
	}
	return nil, 0, false
}

// stringEnd returns the index of the quote that closes the string opening at
// line[open], or -1 when there is none.
func stringEnd(line []byte, open int) int {
	for i := open + 1; i < len(line); {
		j := bytes.IndexByte(line[i:], '"')
		if j < 0 {
			return -1
		}
		j += i
		bs := 0
		for k := j - 1; k > open && line[k] == '\\'; k-- {
			bs++
		}
		if bs%2 == 0 {
			return j
		}
		i = j + 1
	}
	return -1
}

// leanBody remembers, for a record whose description was cut out, which line
// carried it and a fingerprint of that line's bytes.
type leanBody struct {
	off int64
	n   int32
	sum uint64
}

// leanState is the per-read state of a scoped fold.
type leanState struct {
	bodies map[posting.Key]leanBody
	buf    []byte
}

// note records the line (n bytes at off, newline included) that carried k's
// cut-out description.
func (l *leanState) note(k posting.Key, off int64, n int, line []byte) {
	l.bodies[k] = leanBody{off: off, n: int32(n), sum: maphash.Bytes(bodySeed, line)}
}

// read returns k's description from the line note recorded, below limit, after
// checking that line is byte for byte the one that was folded.
func (l *leanState) read(rd io.ReaderAt, k posting.Key, limit int64) (string, error) {
	b, ok := l.bodies[k]
	if !ok || b.n <= 1 || b.n > MaxLineBytes || b.off < 0 || b.off+int64(b.n) > limit {
		return "", fmt.Errorf("%w: %s: no intact line for it", ErrBodyUnavailable, k)
	}
	buf := make([]byte, b.n)
	if _, err := rd.ReadAt(buf, b.off); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrBodyUnavailable, k, err)
	}
	line := buf[:len(buf)-1]
	if buf[len(buf)-1] != '\n' || maphash.Bytes(bodySeed, line) != b.sum {
		return "", fmt.Errorf("%w: %s: the line changed since it was read", ErrBodyUnavailable, k)
	}
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrBodyUnavailable, k, err)
	}
	if e.Key != k || e.Posting == nil || e.Posting.Key != k {
		return "", fmt.Errorf("%w: %s: the line is another event", ErrBodyUnavailable, k)
	}
	return e.Posting.Description, nil
}

// ReadSnapshotScoped is ReadSnapshot that loads no description until asked.
// It folds dir/events.jsonl the way ReadSnapshot does (lock-free, read-only,
// a torn tail left out, mid-file corruption refused), without materialising
// descriptions. It then calls scope once with the records so far (sorted by
// Key; descriptions empty, BodyEvicted true wherever a body was left out) and
// reads back the description of each record keep reports true for. A nil keep
// loads none.
//
// Every record keep rejects stays BodyEvicted with an empty Description; the
// caller may use only its other fields. A body that cannot be read back
// verbatim (the file changed under the read) is an ErrBodyUnavailable error,
// never a record with a missing or different description. ReadSnapshot is
// unchanged and returns every body.
func ReadSnapshotScoped(dir string, scope func(lean Snapshot) (keep func(Record) bool)) (Snapshot, error) {
	ev, err := os.Open(filepath.Join(dir, eventsName))
	if err != nil {
		return Snapshot{}, fmt.Errorf("ledger: %w", err)
	}
	defer ev.Close()
	recs := map[posting.Key]*slot{}
	lean := &leanState{bodies: map[posting.Key]leanBody{}}
	good, torn, err := replayWith(ev, recs, nil, lean)
	if err != nil {
		return Snapshot{}, err
	}
	snap := Snapshot{Records: sorted(recs), Torn: torn}
	if scope == nil {
		return snap, nil
	}
	keep := scope(snap)
	if keep == nil {
		return snap, nil
	}
	for i := range snap.Records {
		r := &snap.Records[i]
		if !r.BodyEvicted || !keep(*r) {
			continue
		}
		d, err := lean.read(ev, r.Key, good)
		if err != nil {
			return Snapshot{}, err
		}
		r.Posting.Description, r.BodyEvicted = d, false
	}
	return snap, nil
}
