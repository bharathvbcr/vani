package main

import (
	"context"
	"encoding/json" // v1, as the rest of cmd/vani: go.mod is at go 1.26 (v2 needs 1.27)
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// maxLabelEmbed bounds how many labelled postings one call embeds; the rest
// wait for the next run.
const maxLabelEmbed = 200

// labelledDocs are the owner-judged postings (would_apply yes, maybe or no; a
// skip labels nothing) whose current text has no vector in the store, sorted
// by key, at most maxLabelEmbed. The warm embeds kept and marked postings only,
// in Score order, so a labelled posting that was dropped, or kept low, may never
// get one; the taste column and the yes-neighbour cell need it (the audit
// benchmark found 31 of 32 judged postings without a vector, 2026-11-14).
//
// Only answered postings: a drawn card nobody answered is not embedded, so a
// draw never puts a posting into the neighbour pool. has reports whether the
// store holds a current vector for a text (nil: there is no store yet); text
// gives a record's description, an evicted one read back. A record whose text
// cannot be read is counted in unreadable and not sent.
//
// hashes are the TextHashes of every judged posting whose text was read,
// embedded now or before: the set pinLabelled keeps in every live set.
func labelledDocs(recs []ledger.Record, labels map[posting.Key]eval.Entry, has func(title, desc string) bool, text func(ledger.Record) (string, error)) (docs []semantic.Doc, hashes []string, unreadable, more int) {
	byKey := make(map[posting.Key]ledger.Record, len(labels))
	for _, r := range recs {
		if _, ok := labels[r.Key]; ok {
			byKey[r.Key] = r
		}
	}
	for _, k := range slices.Sorted(maps.Keys(byKey)) {
		switch labels[k].Labels[eval.WouldApply] {
		case "yes", "maybe", "no":
		default:
			continue
		}
		r := byKey[k]
		desc, err := text(r)
		if err != nil {
			unreadable++
			continue
		}
		hashes = append(hashes, semantic.TextHash(r.Posting.Title, desc))
		if has != nil && has(r.Posting.Title, desc) {
			continue
		}
		if len(docs) == maxLabelEmbed {
			more++
			continue
		}
		docs = append(docs, semantic.Doc{Key: string(k), Title: r.Posting.Title, Description: desc})
	}
	slices.Sort(hashes)
	return docs, slices.Compact(hashes), unreadable, more
}

// labelledPinsName is the file, in the semantic directory, that lists the
// judged postings' TextHashes for pinLabelled.
const labelledPinsName = "labelled-texts.json"

// labelPinWarn is where pinLabelled reports a pin list it could not read.
var labelPinWarn io.Writer = os.Stderr

type labelledPins struct {
	V      int      `json:"v"`
	Hashes []string `json:"text_hashes"`
}

// maxStateFileBytes bounds a read of one of Vani's own small state files
// (the pin list, a removal record): far over any real one, far under memory.
const maxStateFileBytes = 32 << 20

// stateOpenAttempts bounds openStateFile's retries of a lost race.
const stateOpenAttempts = 8

// openStateFile is atomicfile.OpenRead that survives the writer: these files are
// replaced by atomic rename (atomicfile.Write), and OpenRead, which proves the
// file it opened is still the one at the path, fails when a rename lands between
// its open and that check ("kept changing underneath the open"). That failure
// has no sentinel, so any error that is not a deliberate refusal (missing file,
// symbolic link, non-regular file) is retried a bounded number of times: the
// retry reads the old file or the new one, never none. A persistent error is
// returned as it is.
func openStateFile(path string) (*os.File, error) {
	var err error
	for i := 0; i < stateOpenAttempts; i++ {
		var f *os.File
		if f, err = atomicfile.OpenRead(path); err == nil {
			return f, nil
		}
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, atomicfile.ErrLinkedLeaf) || errors.Is(err, atomicfile.ErrNotRegular) {
			return nil, err
		}
	}
	return nil, err
}

// readStateFile reads the regular file at path, at most limit bytes. A missing
// file is os.ErrNotExist; a symbolic link, FIFO or the like is refused by
// atomicfile.OpenRead; a file over limit is an error naming its size and the
// limit, never a truncated read.
func readStateFile(path string, limit int64) ([]byte, error) {
	f, err := openStateFile(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > limit {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit", path, st.Size(), limit)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s grew past the %d-byte limit while it was read", path, limit)
	}
	return b, nil
}

// writeLabelledPins replaces the pin list with hashes through atomicfile (a
// synced temporary file, renamed, the directory synced), so a crash leaves the
// old list or the new one, never none and never half, and a failure leaves no
// temporary file behind.
func writeLabelledPins(stateDir string, hashes []string) error {
	dir := semanticDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(labelledPins{V: 1, Hashes: hashes})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, labelledPinsName), b)
}

// pinLabelled returns ranks with every judged posting's text (the pin list
// embedLabelled writes) ranked above any Score, so a compaction of the vector
// log, by whichever warm makes it (the daily run's, the one before the advance
// phase, the idle warm), keeps the owner's judged postings: the live sets those
// warms name hold browsable postings only, and a judged posting that was
// dropped would otherwise be the first evicted. nil ranks (no live set) stay
// nil. A missing or malformed pin list pins nothing; embedLabelled rewrites it
// on its next run. One that cannot be read (over maxStateFileBytes, a link, an
// I/O error) pins nothing too, and says so on labelPinWarn with its size and the
// limit, because the judged postings are then evicted at the next compaction.
func pinLabelled(ranks map[string]float64, stateDir string) map[string]float64 {
	if ranks == nil {
		return nil
	}
	b, err := readStateFile(filepath.Join(semanticDir(stateDir), labelledPinsName), maxStateFileBytes)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(labelPinWarn, "vani: the label pin list was not read, so judged postings are not pinned: %s\n", oneLine(err.Error(), 300))
		}
		return ranks
	}
	var p labelledPins
	if json.Unmarshal(b, &p) != nil || p.V != 1 || len(p.Hashes) == 0 {
		return ranks
	}
	out := maps.Clone(ranks)
	for _, h := range p.Hashes {
		out[h] = math.Inf(1)
	}
	return out
}

// embedLabelled embeds labelledDocs through the warm's own writer
// (warmSemanticWith), with the run's live set (warmRanked) and the judged
// postings pinned in it (pinLabelled; the pin list is written first). It never
// fails a run: whatever stopped it is in the report.
func embedLabelled(ctx context.Context, cfg config.Config, stateDir string, recs []ledger.Record, mk map[posting.Key]marks.Mark, labels map[posting.Key]eval.Entry, text func(ledger.Record) (string, error)) semanticReport {
	var rep semanticReport
	if !cfg.SemanticOn() {
		return notRun(rep, "semantic.snapshot and kb.bank are not both set", nil)
	}
	var has func(title, desc string) bool
	vr, err := semantic.OpenVectorReader(semanticDir(stateDir))
	switch {
	case err == nil:
		has = func(title, desc string) bool {
			_, st := vr.Pooled(title, desc)
			return st == semantic.PoolOK
		}
	case errors.Is(err, fs.ErrNotExist): // no store yet: every labelled posting is sent
	default:
		return notRun(rep, "the vector store could not be read", err)
	}
	docs, hashes, unreadable, more := labelledDocs(recs, labels, has, text)
	if vr != nil {
		vr.Close() // the warm appends to the log this reader holds open
	}
	var notes []string
	if err := writeLabelledPins(stateDir, hashes); err != nil {
		notes = append(notes, "the judged postings could not be pinned in the vector log's live set: "+oneLine(err.Error(), 200))
	}
	if len(docs) > 0 {
		_, ranks := warmRanked(recs, mk, cfg)
		rep = warmSemanticWith(ctx, cfg, stateDir, docs, ranks, len(docs))
	} else {
		rep.Ran = true
	}
	if rep.Note != "" {
		notes = append([]string{rep.Note}, notes...)
	}
	if unreadable > 0 {
		notes = append(notes, fmt.Sprintf("%d labelled posting(s) not embedded: stored description unreadable", unreadable))
	}
	if more > 0 {
		notes = append(notes, fmt.Sprintf("%d more labelled posting(s) wait for the next run (at most %d a run)", more, maxLabelEmbed))
	}
	rep.Note = strings.Join(notes, "; ")
	return rep
}

// embedLabelledNow reads the labels and runs embedLabelled, logging what it
// did under "labelled postings". Labels that cannot be read are a note, never
// a failed run.
func (e *env) embedLabelledNow(ctx context.Context, cfg config.Config, recs []ledger.Record, mk map[posting.Key]marks.Mark, text func(ledger.Record) (string, error)) semanticReport {
	labels, bad, err := eval.LabelsAt(e.labelsDir())
	if err != nil {
		rep := semanticReport{Note: "labelled postings not embedded: the labels could not be read: " + oneLine(err.Error(), 200)}
		fmt.Fprintf(e.stderr, "vani: semantic: %s\n", rep.Note)
		return rep
	}
	if bad > 0 {
		fmt.Fprintf(e.stderr, "vani: %d malformed label lines skipped\n", bad)
	}
	rep := embedLabelled(ctx, cfg, e.stateDir, recs, mk, labels, text)
	if rep.Ran {
		fmt.Fprintf(e.stderr, "vani: semantic: labelled postings: %s\n", rep.countsLine())
	}
	if rep.Note != "" {
		fmt.Fprintf(e.stderr, "vani: semantic: labelled postings: %s\n", rep.Note)
	}
	return rep
}
