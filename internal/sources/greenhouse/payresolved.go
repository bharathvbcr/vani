package greenhouse

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A completed pay check is remembered by the posting key and the hash of the
// board text (pay lines stripped). An empty answer and a 404 are completed
// checks. A skip or a failed request is not: the posting is asked again, and
// a failure is asked after the ones that were never asked, so a cap, a budget
// or a failure limit moves forward on the next run. Pay lines in the stored
// description are themselves a completed check; they are written here only
// once the ledger already holds them.
//
// The file is not seeded from the ledger. A stored body with no pay lines and
// no entry here has not been checked.

const payResolvedVersion = 1

var (
	// maxPayResolved bounds the file. The corpus is smaller; past the bound the
	// oldest checks are dropped and those postings are asked again.
	maxPayResolved = 100_000
	// maxPayResolvedFile bounds the read. A larger file is unusable, and is
	// replaced with a valid empty one: asking again is safe, trusting it is not.
	maxPayResolvedFile int64 = 16 << 20
)

// payMark is one posting's latest pay check for a board-text hash.
type payMark struct {
	hash    string
	ok      bool
	persist bool // written to the file. A with-pay success stays in memory until the stored description has the lines.
	seq     uint64
}

// payChecks is the source's memory of pay checks. An empty path keeps it for
// this process only.
type payChecks struct {
	path  string
	warn  func(string, ...any)
	marks map[string]payMark
	next  uint64
	// persist is how many marks are written. Session-only marks are not counted.
	persist int
	dirty   bool
}

type payCheckJSON struct {
	K  string `json:"k"`
	H  string `json:"h"`
	OK bool   `json:"ok"`
}

type payDoc struct {
	Version int            `json:"version"`
	Checks  []payCheckJSON `json:"checks"`
}

// loadPayChecks reads path. A missing file is no checks. A file that cannot
// be parsed, is the wrong version, or is over the size bound is no checks
// either, with a warning, and is replaced by a valid empty file. A file that
// cannot be read at all is left in place (replacing it could destroy checks
// that were only unreadable) and this process keeps its own memory.
func loadPayChecks(path string, warn func(string, ...any)) *payChecks {
	c := &payChecks{path: path, warn: warn, marks: map[string]payMark{}}
	if path == "" {
		return c
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c
	}
	if err != nil {
		c.warnf("greenhouse pay checks not read: %v", err)
		c.path = ""
		return c
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPayResolvedFile+1))
	if err != nil {
		c.warnf("greenhouse pay checks not read: %v", err)
		c.path = ""
		return c
	}
	if int64(len(raw)) > maxPayResolvedFile {
		c.warnf("greenhouse pay checks ignored: %s is over %d bytes", path, maxPayResolvedFile)
		c.dirty = true
		c.save()
		return c
	}
	var doc payDoc
	if json.Unmarshal(raw, &doc) != nil || doc.Version != payResolvedVersion {
		c.warnf("greenhouse pay checks ignored: %s is not a version %d file", path, payResolvedVersion)
		c.dirty = true
		c.save()
		return c
	}
	dropped := 0
	for _, rec := range doc.Checks {
		if !validPayKey(rec.K) || !validPayHash(rec.H) {
			dropped++
			continue
		}
		if _, exists := c.marks[rec.K]; exists {
			dropped++
		}
		c.next++
		c.marks[rec.K] = payMark{hash: rec.H, ok: rec.OK, persist: true, seq: c.next}
	}
	c.persist = len(c.marks)
	if c.persist > maxPayResolved {
		dropped += c.persist - maxPayResolved
		c.evict()
	}
	if dropped > 0 {
		c.dirty = true
		c.warnf("greenhouse pay checks: %d unusable or surplus entries dropped from %s", dropped, path)
	}
	if c.dirty {
		c.save()
	}
	return c
}

func (c *payChecks) warnf(format string, args ...any) {
	if c != nil && c.warn != nil {
		c.warn(format, args...)
	}
}

// done reports a completed check for this posting's board text.
func (c *payChecks) done(key posting.Key, base string) bool {
	if c == nil {
		return false
	}
	m, ok := c.marks[string(key)]
	return ok && m.ok && m.hash == payBodyHash(base)
}

// failed reports a request that failed for this board text, so the posting is
// asked after the ones that have never been asked.
func (c *payChecks) failed(key posting.Key, base string) bool {
	if c == nil {
		return false
	}
	m, ok := c.marks[string(key)]
	return ok && !m.ok && m.hash == payBodyHash(base)
}

// remember records a check. persist writes it. A with-pay success passes
// persist false: the lines are not durable until a later run reads them back
// from the stored posting, and recording them now would survive a crash that
// never stored the lines.
func (c *payChecks) remember(key posting.Key, base string, ok, persist bool) {
	if c == nil {
		return
	}
	k := string(key)
	if !validPayKey(k) {
		return
	}
	h := payBodyHash(base)
	old, exists := c.marks[k]
	if exists && old.hash == h && old.ok == ok && old.persist == persist {
		return
	}
	m := payMark{hash: h, ok: ok, persist: persist}
	if persist {
		c.next++
		m.seq = c.next
		if !exists || !old.persist {
			c.persist++
		}
		c.dirty = true
	} else if exists && old.persist {
		c.persist--
		c.dirty = true
	}
	c.marks[k] = m
	c.evict()
}

// evict drops the oldest persisted checks past the bound. The same sequence
// number keeps the key that sorts first, so the choice does not depend on map
// iteration.
func (c *payChecks) evict() {
	for c.persist > maxPayResolved {
		var oldest string
		var seq uint64
		found := false
		for k, m := range c.marks {
			if !m.persist {
				continue
			}
			if !found || m.seq < seq || (m.seq == seq && k < oldest) {
				found = true
				oldest = k
				seq = m.seq
			}
		}
		if !found {
			return
		}
		delete(c.marks, oldest)
		c.persist--
		c.dirty = true
	}
}

// save writes the persisted checks when they changed. A failed write stays
// dirty so the next board tries again. Session-only marks are not written.
func (c *payChecks) save() {
	if c == nil || c.path == "" || !c.dirty {
		return
	}
	type row struct {
		rec payCheckJSON
		seq uint64
	}
	rows := make([]row, 0, c.persist)
	for k, m := range c.marks {
		if !m.persist || !validPayKey(k) || !validPayHash(m.hash) {
			continue
		}
		rows = append(rows, row{rec: payCheckJSON{K: k, H: m.hash, OK: m.ok}, seq: m.seq})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].seq != rows[j].seq {
			return rows[i].seq < rows[j].seq
		}
		return rows[i].rec.K < rows[j].rec.K
	})
	out := make([]payCheckJSON, len(rows))
	for i := range rows {
		out[i] = rows[i].rec
	}
	raw, err := json.MarshalIndent(payDoc{Version: payResolvedVersion, Checks: out}, "", "  ")
	if err != nil {
		c.warnf("greenhouse pay checks not saved: %v", err)
		return
	}
	if err := atomicfile.Write(c.path, append(raw, '\n')); err != nil {
		c.warnf("greenhouse pay checks not saved: %v", err)
		return
	}
	c.dirty = false
}

func payBodyHash(base string) string {
	return posting.Posting{Description: srcutil.SourceText(base)}.DescriptionHash()
}

func validPayKey(k string) bool {
	if k == "" || len(k) > 512 {
		return false
	}
	for _, r := range k {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validPayHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, r := range h {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
