// Package jobfacts keeps what the Jobs views derive from a posting's text
// alone, so a list needs no description it has derived from before: the
// keyword hits (jobinsight.Vocab.Extract), the semantic text hash
// (semantic.TextHash), the manual-reading text hash (manualtriage.TextSHA256),
// the stated pay (jobinsight.PostingPay) and the posting half of the
// knowledge-base match identity (kbmatch.TextPrefix).
//
// Each posting's facts are filed under its key with the Input they were
// derived from: the description's fingerprint as the events file holds it
// (ledger.BodyFingerprint) and the other posting fields a deriver reads. A
// fact is answered only while its Input equals the posting's now, and only
// from a store whose Version is this build's (Version hashes the vocabulary
// and every deriver's answers over a fixed probe), so a changed posting or a
// changed deriver is derived again, never served stale.
//
// The store is a cache. It lives beside the ledger, is written atomically by
// whichever read derived something new, under a lock of its own taken without
// waiting (a writer that finds it held skips, and the next read writes). Two
// writers racing write whole files; the later rename wins and the loser's new
// facts are derived again on a later read.
package jobfacts

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/expyears"
	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// Facts are what the Jobs views derive from one posting's text.
type Facts struct {
	Hits       []jobinsight.Hit // Extract(title + "\n" + description)
	TextHash   string           // semantic.TextHash(title, description)
	ManualText string           // manualtriage.TextSHA256(posting)
	Pay        []jobinsight.Pay // jobinsight.PostingPay(posting)
	KBText     []byte           // kbmatch.TextPrefix(posting)
	// StaffLevel is the level expyears.BelowStaff gives the description ("" when
	// the text states no years, or years at staff level): the ranking's
	// staff-level penalty, read from here so a record held without its text
	// ranks as one held whole.
	StaffLevel string
}

// Input is everything Facts depend on: the description by fingerprint, and
// the posting fields the derivers read besides it.
type Input struct {
	BodyFP          [16]byte
	Title, Location string
	Remote          bool
}

// InputOf is p's Input, its description's fingerprint being fp.
func InputOf(p posting.Posting, fp [16]byte) Input {
	return Input{BodyFP: fp, Title: p.Title, Location: p.Location, Remote: p.Remote}
}

// Derive computes p's facts. p must carry its description.
func Derive(v *jobinsight.Vocab, p posting.Posting) Facts {
	level, _ := expyears.BelowStaff(p.Description)
	return Facts{
		StaffLevel: level,
		Hits:       v.Extract(p.Title + "\n" + p.Description),
		TextHash:   semantic.TextHash(p.Title, p.Description),
		ManualText: manualtriage.TextSHA256(p),
		Pay:        jobinsight.PostingPay(p),
		KBText:     kbmatch.TextPrefix(p),
	}
}

// format changes whenever the stored layout, or what a field means, does.
const format = 2

// Version identifies the derivers: the vocabulary, and each deriver's answers
// over a probe that exercises them (titles, locations, pay forms, odd text).
// A store written under another Version is not read.
func Version(v *jobinsight.Vocab) string {
	h := sha256.New()
	fmt.Fprintf(h, "jobfacts format %d\x00vocab %s\x00", format, v.Fingerprint())
	enc := gob.NewEncoder(h)
	for _, p := range probe {
		if err := enc.Encode(Derive(v, p)); err != nil {
			panic(fmt.Sprintf("jobfacts: probe facts do not encode: %v", err)) // a programming error
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

var probe = []posting.Posting{
	{Title: "Senior Inference Engineer (Go, Rust)", Location: "San Francisco, CA", Description: "We run LLM inference on Kubernetes with CUDA and PyTorch. Salary: $180,000 - $240,000 per year. Python, Go, Rust, gRPC, observability."},
	{Title: "ML Platform Engineer", Location: "Toronto, ON", Remote: true, Description: "Remote in Canada. CAD 140k–170k. Terraform, AWS, Spark, Airflow, k8s, MLOps."},
	{Title: "Staff Engineer", Location: "Berlin, Germany", Description: "€90.000 – €120.000 brutto pro Jahr. TypeScript, React, PostgreSQL. Ünïcödé — 工程师 🚀"},
	{Title: "Contractor", Location: "Remote - US", Remote: true, Description: "USD 60-85/hour, W2. Java, Spring, Kafka.\n\nBenefits: 401(k)."},
	{Title: "Staff Engineer", Location: "Remote - US", Remote: true, Description: "Requires 3+ years of experience in Go."},
	{Title: "", Location: "", Description: ""},
}

// Store is the saved facts, by posting key.
type Store struct {
	mu      sync.Mutex
	path    string
	version string
	m       map[posting.Key]entry
	dirty   bool
	loaded  string // how Open found the file, for Info
	// changes counts the edits to m (Put, and each key Prune forgets).
	changes uint64
}

type entry struct {
	In Input
	F  Facts
}

type storeFile struct {
	Format  int
	Version string
	Entries []storeEntry
}

// storeEntry is one posting's facts as saved. Gob writes an empty slice and a
// nil one alike, and JSON does not ([] against null), so which of the two each
// slice was is saved beside it and restored on load.
type storeEntry struct {
	Key             posting.Key
	In              Input
	F               Facts
	HitsSet, PaySet bool // F.Hits / F.Pay were non-nil
}

// maxStoreBytes bounds a store read: larger is not a file this package wrote.
const maxStoreBytes = 1 << 30

// Open reads the store at path for version. A store that is missing, of
// another version or unreadable starts empty; Info says which.
func Open(path, version string) *Store {
	s := &Store{path: path, version: version, m: map[posting.Key]entry{}}
	s.loaded = s.load()
	return s
}

func (s *Store) load() string {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return "empty: no store yet"
	}
	if err != nil {
		return "empty: " + err.Error()
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "empty: " + err.Error()
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxStoreBytes {
		return "empty: the store is not a regular file of a plausible size"
	}
	var sf storeFile
	if err := gob.NewDecoder(bufio.NewReaderSize(f, 1<<20)).Decode(&sf); err != nil {
		return "empty: unreadable: " + err.Error()
	}
	if sf.Format != format || sf.Version != s.version {
		return "empty: written by another version of the derivers"
	}
	terms := map[string]string{}
	intern := func(x string) string {
		if c, ok := terms[x]; ok {
			return c
		}
		terms[x] = x
		return x
	}
	for _, e := range sf.Entries {
		if e.HitsSet && e.F.Hits == nil {
			e.F.Hits = []jobinsight.Hit{}
		}
		if e.PaySet && e.F.Pay == nil {
			e.F.Pay = []jobinsight.Pay{}
		}
		for i := range e.F.Hits {
			e.F.Hits[i].Term, e.F.Hits[i].Category = intern(e.F.Hits[i].Term), intern(e.F.Hits[i].Category)
		}
		s.m[e.Key] = entry{In: e.In, F: e.F}
	}
	return "loaded"
}

// Get returns k's facts when they were derived from in.
func (s *Store) Get(k posting.Key, in Input) (Facts, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[k]
	if !ok || e.In != in {
		return Facts{}, false
	}
	return e.F, true
}

// Put files k's facts, derived from in.
func (s *Store) Put(k posting.Key, in Input, f Facts) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = entry{In: in, F: f}
	s.dirty = true
	s.changes++
}

// Prune forgets every key live does not name, so the store holds only
// postings a view can read; a later Save writes the smaller set.
func (s *Store) Prune(live func(posting.Key) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.m {
		if !live(k) {
			delete(s.m, k)
			s.dirty = true
			s.changes++
		}
	}
}

// Changes counts the edits made to the store since it was opened: two equal
// counts mean every Get between them answered from the same facts.
func (s *Store) Changes() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changes
}

// Dirty is whether the store holds edits Save has not written.
func (s *Store) Dirty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirty
}

// Len is how many postings have facts.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// Info is how the store was found when opened ("loaded", or "empty: <why>").
func (s *Store) Info() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded
}

// ErrBusy means another process is writing the store; the save was skipped.
var ErrBusy = errors.New("jobfacts: another process is writing the store")

// Save writes the store if anything changed since it was opened or saved.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	lock, err := filelock.TryAcquire(s.path + ".lock")
	if errors.Is(err, filelock.ErrWouldBlock) {
		return ErrBusy
	}
	if err != nil {
		return fmt.Errorf("jobfacts: lock: %w", err)
	}
	defer lock.Release()
	sf := storeFile{Format: format, Version: s.version, Entries: make([]storeEntry, 0, len(s.m))}
	for k, e := range s.m {
		sf.Entries = append(sf.Entries, storeEntry{Key: k, In: e.In, F: e.F, HitsSet: e.F.Hits != nil, PaySet: e.F.Pay != nil})
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(&sf); err != nil {
		return fmt.Errorf("jobfacts: encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("jobfacts: %w", err)
	}
	if err := atomicfile.Write(s.path, buf.Bytes()); err != nil {
		return fmt.Errorf("jobfacts: %w", err)
	}
	s.dirty = false
	return nil
}
