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
// whenever the line is not unambiguously in the shape the ledger writer
// produces; the caller then decodes the line whole, so an unusual line is never
// misread, only not shortened. The cut is made only when all of these hold:
//
//   - there is no whitespace outside strings (Marshal's output is compact), so
//     no key is separated from its colon or value;
//   - no object key contains a backslash or a byte >= 0x80 (a key spelled with
//     escapes, or one that matches only by Unicode case folding, would be read
//     by encoding/json as a different name than the scan sees);
//   - exactly one top-level key equals "posting" and exactly one key of that
//     object equals "description", both ignoring ASCII case (encoding/json
//     matches field names case-insensitively and takes the last duplicate, so
//     any repeat makes the cut ambiguous);
//   - the description is a string whose contents are valid JSON string
//     contents (legal escapes, no control characters), so a line ReadSnapshot
//     refuses as corrupt is not shortened here and is refused the same way.
//
// The structural scan jumps from quote to quote, so only the description is
// examined byte by byte, and only for validity; it is never copied or tokenised.
func stripDescription(line, buf []byte) (lean []byte, cut int, ok bool) {
	depth, inPosting := 0, false
	postings, descs := 0, 0
	dstart, dend := 0, 0
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == '{' || c == '[':
			depth++
			i++
		case c == '}' || c == ']':
			depth--
			i++
		case c <= ' ':
			return nil, 0, false // whitespace outside a string: not a canonical line
		case c == '"':
			end := stringEnd(line, i)
			if end < 0 {
				return nil, 0, false
			}
			name := line[i+1 : end] // raw contents
			i = end + 1
			if i >= len(line) || line[i] != ':' {
				continue // a value, not a key
			}
			i++
			if !plainKey(name) {
				return nil, 0, false
			}
			switch {
			case depth == 1:
				inPosting = bytes.EqualFold(name, postingKeyB)
				if inPosting {
					if postings++; postings > 1 {
						return nil, 0, false
					}
				}
			case depth == 2 && inPosting && bytes.EqualFold(name, descriptionKeyB):
				if descs++; descs > 1 || i >= len(line) || line[i] != '"' {
					return nil, 0, false
				}
				vend := stringEnd(line, i)
				if vend < 0 || !validStringBody(line[i+1:vend]) {
					return nil, 0, false
				}
				dstart, dend = i+1, vend
				i = vend + 1
			}
		default:
			i++
		}
	}
	if descs != 1 {
		return nil, 0, false
	}
	buf = append(buf[:0], line[:dstart]...)
	buf = append(buf, line[dend:]...)
	return buf, dend - dstart, true
}

var (
	postingKeyB     = []byte(postingKey)
	descriptionKeyB = []byte(descriptionKey)
)

// plainKey reports whether an object key's raw contents are what the decoder
// will read: ASCII with no escapes.
func plainKey(name []byte) bool {
	for _, c := range name {
		if c == '\\' || c >= 0x80 {
			return false
		}
	}
	return true
}

// validStringBody reports whether s (the bytes between a string's quotes) is
// valid JSON string content by encoding/json's rules: no control character
// below 0x20, and every backslash starts one of \" \\ \/ \b \f \n \r \t or
// \uXXXX. Invalid UTF-8 is not an error to the decoder (it becomes U+FFFD), so
// it is accepted here too.
func validStringBody(s []byte) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= ' ' && c != '\\' {
			continue
		}
		if c != '\\' {
			return false
		}
		i++
		if i >= len(s) {
			return false
		}
		switch s[i] {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			if i+4 >= len(s) {
				return false
			}
			for _, h := range s[i+1 : i+5] {
				if !('0' <= h && h <= '9' || 'a' <= h && h <= 'f' || 'A' <= h && h <= 'F') {
					return false
				}
			}
			i += 4
		default:
			return false
		}
	}
	return true
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
	whole  int // lines decoded whole because stripDescription would not cut them
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
