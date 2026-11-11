package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
	"github.com/bharathvbcr/Vani/internal/jobfacts"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// jobsRead is the Jobs views' read of the state: the snapshot and marks
// jobsState returns, and the text facts (internal/jobfacts) of every record a
// view reads the text of. A record whose facts were stored and still match it
// keeps its description in the events file (BodyEvicted), so the read loads
// only the descriptions of postings that are new or changed since a read last
// derived them.
type jobsRead struct {
	snap  ledger.Snapshot
	m     map[posting.Key]marks.Entry
	facts *textFacts
}

// textFacts is each in-scope record's facts for one read. The slices in a
// Facts are shared by every reader and never changed.
type textFacts struct {
	m map[posting.Key]jobfacts.Facts
	// Served is how many came from the store, Derived how many were derived
	// from text this read (and saved for the next).
	Served, Derived int
	// Store is how the store was found ("loaded", "empty: <why>"), Saved
	// what saving it did ("saved", "unchanged", "skipped: ...").
	Store, Saved string

	mu      sync.Mutex
	missing []posting.Key // lean records a deriver asked for (factsOf)
}

// jobsFactsPath is where the Jobs views keep their facts: beside the ledger
// checkpoint.
func (e *env) jobsFactsPath() string { return filepath.Join(e.stateDir, "readmodel", "jobfacts") }

// jobsResident is what a long-lived Jobs reader (`vani jobs serve`) keeps
// between reads: one ledger Reader, advanced to the events file's end before
// each read, and the facts store, opened once. A command run once has none and
// opens both for its one read.
type jobsResident struct {
	rd    *ledger.Reader
	store *jobfacts.Store
}

// openJobsResident opens the ledger Reader (from the jobs checkpoint) and the
// facts store for e's state.
func (e *env) openJobsResident() (*jobsResident, error) {
	rd, err := ledger.OpenReader(filepath.Join(e.stateDir, "ledger"), e.jobsReaderOptions())
	if err != nil {
		return nil, fmt.Errorf("jobs: read the ledger: %w", err)
	}
	v, err := jobinsight.Default()
	if err != nil {
		rd.Close()
		return nil, err
	}
	return &jobsResident{rd: rd, store: jobfacts.Open(e.jobsFactsPath(), jobfacts.Version(v))}, nil
}

// Close releases the events file.
func (r *jobsResident) Close() error { return r.rd.Close() }

// jobsReader is the ledger Reader for one Jobs read and its release: the
// resident one, advanced to the events file's end, or a new one.
func (e *env) jobsReader() (*ledger.Reader, func(), error) {
	if e.resident != nil {
		if _, err := e.resident.rd.Advance(); err != nil {
			return nil, nil, fmt.Errorf("jobs: read the ledger: %w", err)
		}
		return e.resident.rd, func() {}, nil
	}
	rd, err := ledger.OpenReader(filepath.Join(e.stateDir, "ledger"), e.jobsReaderOptions())
	if err != nil {
		return nil, nil, fmt.Errorf("jobs: read the ledger: %w", err)
	}
	return rd, func() { rd.Close() }, nil
}

// jobsFactsStore is the facts store for one Jobs read: the resident one, or
// the one on disk opened now.
func (e *env) jobsFactsStore(v *jobinsight.Vocab) *jobfacts.Store {
	if e.resident != nil {
		return e.resident.store
	}
	return jobfacts.Open(e.jobsFactsPath(), jobfacts.Version(v))
}

// jobsListRead is jobsState for the Jobs views (list, stats, gaps): the same
// snapshot, marks and exclusions, with text facts in place of descriptions
// wherever the stored facts still describe the posting.
//
// A record is read whole (its description loaded) when it is in jobsTextScope
// and has no current stored facts, or when the config's exclusions read
// descriptions (config.Exclude.ReadsDescriptions): then every in-scope record
// is read whole, as jobsState reads it, because an exclusion or the prefilter
// must match its text. Facts derived here are saved for the next read, and the
// store forgets postings no view reads.
func (e *env) jobsListRead() (jobsRead, error) {
	rd, release, err := e.jobsReader()
	if err != nil {
		return jobsRead{}, err
	}
	defer release()
	snap := rd.Snapshot()
	m, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		return jobsRead{}, fmt.Errorf("jobs: read marks: %w", err)
	}
	cfg, cfgErr := e.loadConfig()
	wholeText := cfgErr == nil && cfg.Exclude.ReadsDescriptions()
	v, err := jobinsight.Default()
	if err != nil {
		return jobsRead{}, err
	}
	store := e.jobsFactsStore(v)
	tf := &textFacts{m: map[posting.Key]jobfacts.Facts{}, Store: store.Info()}

	keep := jobsTextScope(m)
	inScope := make(map[posting.Key]bool, len(snap.Records))
	need := map[posting.Key]bool{}
	for _, r := range snap.Records {
		if !keep(r) {
			continue
		}
		inScope[r.Key] = true
		if fp, ok := rd.BodyFP(r.Key); ok {
			if f, ok := store.Get(r.Key, jobfacts.InputOf(r.Posting, fp)); ok {
				tf.m[r.Key] = f
				tf.Served++
				if !wholeText {
					continue
				}
			}
		}
		need[r.Key] = true
	}
	if len(need) > 0 {
		if err := rd.Hydrate(snap.Records, func(r ledger.Record) bool { return need[r.Key] }); err != nil {
			return jobsRead{}, fmt.Errorf("jobs: read the ledger: %w", err)
		}
	}
	for _, r := range snap.Records {
		if !need[r.Key] || r.BodyEvicted {
			continue
		}
		if _, ok := tf.m[r.Key]; ok {
			continue // read whole for the exclusions; its facts were current
		}
		fp, ok := rd.BodyFP(r.Key)
		if !ok {
			continue // no event carried a posting: nothing to file it under
		}
		f := jobfacts.Derive(v, r.Posting)
		store.Put(r.Key, jobfacts.InputOf(r.Posting, fp), f)
		tf.m[r.Key] = f
		tf.Derived++
	}
	store.Prune(func(k posting.Key) bool { return inScope[k] })
	switch err := store.Save(); {
	case err == nil:
		tf.Saved = "saved or unchanged"
	case errors.Is(err, jobfacts.ErrBusy):
		tf.Saved = "skipped: another read is saving"
	default:
		// A cache that cannot be written costs the next read time, never an
		// answer: say so, and list.
		tf.Saved = "not saved: " + err.Error()
		fmt.Fprintf(e.stderr, "vani: jobs: text facts not saved (the next list derives them again): %v\n", err)
	}
	if cfgErr == nil {
		hideExcluded(snap.Records, m, cfg.Exclude)
	}
	e.lastFacts = tf
	return jobsRead{snap: snap, m: m, facts: tf}, nil
}

// jobsView is jobsListRead and its insight, for one Jobs view command. The
// command calls in.facts.err() once it has built its reply.
func (e *env) jobsView(halfLife float64) (jobsRead, *insight, error) {
	rd, err := e.jobsListRead()
	if err != nil {
		return jobsRead{}, nil, err
	}
	in, err := e.newListInsight(halfLife, rd)
	if err != nil {
		return jobsRead{}, nil, err
	}
	return rd, in, nil
}

// get is k's facts for this read.
func (tf *textFacts) get(k posting.Key) (jobfacts.Facts, bool) {
	if tf == nil {
		return jobfacts.Facts{}, false
	}
	f, ok := tf.m[k]
	return f, ok
}

// noteMissing records a lean record a deriver was asked about: its text is
// neither loaded nor stored, so any answer would be one about an empty
// description. jobsListRead loads or files the text of every record in its
// scope, so this is a read that let one through; err reports it.
func (tf *textFacts) noteMissing(k posting.Key) {
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if len(tf.missing) < 8 {
		tf.missing = append(tf.missing, k)
	}
}

// err is non-nil when a deriver was asked about a record with no text.
func (tf *textFacts) err() error {
	if tf == nil {
		return nil
	}
	tf.mu.Lock()
	defer tf.mu.Unlock()
	if len(tf.missing) == 0 {
		return nil
	}
	return fmt.Errorf("jobs: the text of %s was neither read nor stored (a read-model bug: run `vani jobs list` again, and report it if it repeats)", tf.missing[0])
}

// storedFacts is r's facts from this insight's read, which every deriver site
// (hitsOf, payOf, textHashOf, the manual-reading state) asks first. With a
// facts read (jobsListRead) a record in its scope always has them; a lean
// record without them is noted (textFacts.err, which fails the command) and
// answered from its empty text, as a read without facts answers it. Without a
// facts read every site derives from the record's text.
func (in *insight) storedFacts(r ledger.Record) (jobfacts.Facts, bool) {
	if in.facts == nil {
		return jobfacts.Facts{}, false
	}
	if f, ok := in.facts.get(r.Key); ok {
		return f, true
	}
	if r.BodyEvicted {
		in.facts.noteMissing(r.Key)
	}
	return jobfacts.Facts{}, false
}

// hitsOf is r's keyword hits over title and description.
func (in *insight) hitsOf(r ledger.Record) []jobinsight.Hit {
	if f, ok := in.storedFacts(r); ok {
		return f.Hits
	}
	return in.vocab.Extract(r.Posting.Title + "\n" + r.Posting.Description)
}

// payOf is r's stated pay.
func (in *insight) payOf(r ledger.Record) []jobinsight.Pay {
	if f, ok := in.storedFacts(r); ok {
		return f.Pay
	}
	return jobinsight.PostingPay(r.Posting)
}

// kbHashOf is r's knowledge-base match identity (kbmatch.TextSHA256) under
// summary and d, finished from the stored posting half when there is one. A
// stored half that does not resume is a damaged fact: a lean record is then
// noted as missing, as one with no facts is.
func (in *insight) kbHashOf(r ledger.Record, summary string, d kbmatch.Digest) string {
	if f, ok := in.storedFacts(r); ok {
		if h, err := kbmatch.TextSHA256Of(f.KBText, summary, d); err == nil {
			return h
		}
		if r.BodyEvicted {
			in.facts.noteMissing(r.Key)
		}
	}
	return kbmatch.TextSHA256(r.Posting, summary, d)
}

// textHashOf is r's semantic text hash (semantic.TextHash).
func (in *insight) textHashOf(r ledger.Record) string {
	if f, ok := in.storedFacts(r); ok {
		return f.TextHash
	}
	return semantic.TextHash(r.Posting.Title, r.Posting.Description)
}
