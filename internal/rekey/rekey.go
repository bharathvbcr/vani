// Package rekey is the one-time migration that moves fallback-keyed (x:)
// postings to the keys posting.FallbackKey gives them once titles keep the
// punctuation that names a language (C++, C#, .NET). Only an x: key is a
// fallback hash: every other ATS keys by the board's own id or, for careers
// pages, by the posting URL, and none of those is touched.
//
// No adapter stores x: keys today, so on today's ledgers the migration
// changes nothing; it exists, tested, for the day fallback keys are stored.
//
// It is careful in this order: the ledger is opened (its exclusive lock, and
// a full check that every line reads), the plan is made and refused outright
// on any collision, the events file is backed up, a pending marker records
// the old -> new map with the file's hash before and after, the events file
// is replaced atomically, the user's marks follow their postings, their resume
// folders (internal/resumestore, keyed by a hash of the posting key) follow
// them too, and the marker is marked done. A resume folder already under a new
// key is a collision, refused with the marks'. A second run is refused; a run that died part way
// is finished by the next one, which can tell from the hashes where it
// stopped. The marker and the backup are what reverse it.
package rekey

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

const (
	// MarkerName is the file, in the ledger directory, that records a run.
	MarkerName    = "rekey-fallback-title.json"
	markerVersion = 1
	eventsName    = "events.jsonl"
	backupPrefix  = "events.jsonl.pre-rekey-"
)

var (
	// ErrAlreadyRun means the marker says the migration finished.
	ErrAlreadyRun = errors.New("rekey: the migration already ran on this ledger")
	// ErrCollision means two postings would share a key after re-keying.
	ErrCollision = errors.New("rekey: re-keying would merge postings")
	// ErrInterrupted means a run died part way and the ledger changed since,
	// so it can be neither finished nor redone safely.
	ErrInterrupted = errors.New("rekey: an earlier run did not finish and the ledger has changed since")
)

// Change is one posting's move.
type Change struct {
	Old posting.Key `json:"old"`
	New posting.Key `json:"new"`
}

// Report is what a run found and did.
type Report struct {
	Records  int // postings in the ledger
	Fallback int // of them, x: keyed
	// Changes are the x: postings whose key moves, sorted by old key.
	Changes []Change
	// Unchanged are x: postings whose key is the same under both folds.
	Unchanged int
	// Foreign are x: keys that are not the old fold of their own posting's
	// company, title and location (written some other way, or the fields
	// changed since); they are left alone and listed.
	Foreign []posting.Key
	// MarksMoved counts postings whose marks or note moved with them.
	MarksMoved int
	// ResumesMoved counts postings whose resume folder (internal/resumestore)
	// moved with them in this run.
	ResumesMoved int
	// Backup is the events file's copy from before the run ("" in a dry run).
	Backup string
	DryRun bool
	// Resumed is true when this run finished one that had died part way.
	Resumed bool
}

type marker struct {
	V      int       `json:"v"`
	State  string    `json:"state"` // "pending" or "done"
	At     time.Time `json:"at"`
	Backup string    `json:"backup"`
	// Before and After are the events file's sha256 before and after the
	// rewrite: a later run compares the file with them to see where an
	// interrupted run stopped.
	Before  string   `json:"before_sha256"`
	After   string   `json:"after_sha256"`
	Changes []Change `json:"changes"`
}

// ErrUnsupported is Run's refusal on Windows.
var ErrUnsupported = errors.New("rekey: not supported on Windows: the ledger stays open (locked) while its events file is replaced, and Windows cannot rename over an open file; run it on macOS or Linux")

// goos is runtime.GOOS; a variable only so a test can take the Windows branch
// on any host.
var goos = runtime.GOOS

// crash is a test seam: it is called at each step and a non-nil error stops
// the run there, as a crash would.
var crash = func(step string) error { return nil }

// Run migrates the ledger in stateDir/ledger and the marks in stateDir/marks.
// With dryRun it plans, checks for collisions and reports, writing nothing.
//
// On Windows Run refuses, before it reads or writes anything: it holds the
// ledger open (the lock) while finish renames a new events file over it, and
// Windows cannot rename over an open file. Run it on a Unix host.
func Run(stateDir string, dryRun bool, now time.Time) (Report, error) {
	if goos == "windows" {
		return Report{DryRun: dryRun}, ErrUnsupported
	}
	dir := filepath.Join(stateDir, "ledger")
	if _, err := os.Stat(filepath.Join(dir, eventsName)); err != nil {
		return Report{DryRun: dryRun}, fmt.Errorf("rekey: no ledger in %s: %w", dir, err)
	}
	l, err := ledger.Open(dir) // the exclusive lock, and every line checked
	if err != nil {
		return Report{DryRun: dryRun}, fmt.Errorf("rekey: %w", err)
	}
	defer l.Close()
	if l.Recovered {
		return Report{DryRun: dryRun}, errors.New("rekey: the ledger ends in a torn line; let Vani cut it (any append does) before migrating")
	}

	mpath := filepath.Join(dir, MarkerName)
	prev, err := readMarker(mpath)
	if err != nil {
		return Report{DryRun: dryRun}, err
	}
	if prev != nil && prev.State == "done" {
		return Report{DryRun: dryRun}, fmt.Errorf("%w (%s, %s)", ErrAlreadyRun, prev.At.Format(time.RFC3339), mpath)
	}

	rep := plan(l.All())
	rep.DryRun = dryRun
	markDir := marks.Dir(stateDir)
	current, err := marks.Read(markDir)
	if err != nil {
		return rep, fmt.Errorf("rekey: marks: %w", err)
	}
	rs, err := resumestore.Open(stateDir)
	if err != nil {
		return rep, fmt.Errorf("rekey: resumes: %w", err)
	}
	if prev != nil {
		return resume(rep, dir, markDir, rs, prev, now, dryRun)
	}
	if err := collisions(rep.Changes, l, current, rs); err != nil {
		return rep, err
	}
	if dryRun {
		return rep, nil
	}

	path := filepath.Join(dir, eventsName)
	before, err := os.ReadFile(path)
	if err != nil {
		return rep, fmt.Errorf("rekey: read events: %w", err)
	}
	after, err := rewrite(before, rep.Changes)
	if err != nil {
		return rep, err
	}
	rep.Backup = filepath.Join(dir, backupPrefix+now.UTC().Format("20060102T150405Z"))
	if _, err := os.Lstat(rep.Backup); err == nil {
		return rep, fmt.Errorf("rekey: backup %s already exists", rep.Backup)
	}
	if err := atomicfile.Write(rep.Backup, before); err != nil {
		return rep, fmt.Errorf("rekey: backup: %w", err)
	}
	if err := crash("backup"); err != nil {
		return rep, err
	}
	m := &marker{V: markerVersion, State: "pending", At: now.UTC(), Backup: filepath.Base(rep.Backup),
		Before: sum(before), After: sum(after), Changes: rep.Changes}
	if err := writeMarker(mpath, m); err != nil {
		return rep, err
	}
	if err := crash("pending"); err != nil {
		return rep, err
	}
	return finish(rep, dir, markDir, rs, m, after, now)
}

// resume finishes a run that wrote its pending marker and then stopped. The
// events file is either still the old one (Before), in which case the stored
// map is applied again, or already the new one (After); anything else means
// the ledger moved on and nothing is done.
func resume(rep Report, dir, markDir string, rs *resumestore.Store, m *marker, now time.Time, dryRun bool) (Report, error) {
	rep.Resumed, rep.Backup, rep.Changes = true, filepath.Join(dir, m.Backup), m.Changes
	path := filepath.Join(dir, eventsName)
	cur, err := os.ReadFile(path)
	if err != nil {
		return rep, fmt.Errorf("rekey: read events: %w", err)
	}
	var after []byte
	switch sum(cur) {
	case m.After:
	case m.Before:
		if after, err = rewrite(cur, m.Changes); err != nil {
			return rep, err
		}
		if sum(after) != m.After {
			return rep, fmt.Errorf("%w: rewriting the old file again does not give the recorded result", ErrInterrupted)
		}
	default:
		return rep, fmt.Errorf("%w: restore %s or finish by hand (the marker %s lists every move)", ErrInterrupted, rep.Backup, MarkerName)
	}
	if dryRun {
		return rep, nil
	}
	return finish(rep, dir, markDir, rs, m, after, now)
}

// finish replaces the events file (unless after is nil: already replaced),
// moves the marks, and marks the run done. Each step can be repeated.
func finish(rep Report, dir, markDir string, rs *resumestore.Store, m *marker, after []byte, now time.Time) (Report, error) {
	if after != nil {
		if err := atomicfile.Write(filepath.Join(dir, eventsName), after); err != nil {
			return rep, fmt.Errorf("rekey: replace events: %w", err)
		}
	}
	if err := crash("ledger"); err != nil {
		return rep, err
	}
	n, err := moveMarks(markDir, m.Changes, now)
	rep.MarksMoved = n
	if err != nil {
		return rep, err
	}
	rn, err := moveResumes(rs, m.Changes)
	rep.ResumesMoved = rn
	if err != nil {
		return rep, err
	}
	m.State = "done"
	if err := writeMarker(filepath.Join(dir, MarkerName), m); err != nil {
		return rep, err
	}
	return rep, nil
}

// plan sorts the ledger's x: postings into moves, keys that stay, and keys
// that are not the old fold of their own fields.
func plan(recs []ledger.Record) Report {
	rep := Report{Records: len(recs)}
	for _, r := range recs {
		if r.Key.ATS() != posting.Other {
			continue
		}
		rep.Fallback++
		p := r.Posting
		old, okOld := fallbackV1(p.Company, p.Title, p.Location)
		nk, err := posting.FallbackKey(p.Company, p.Title, p.Location)
		switch {
		case err == nil && nk == r.Key:
			rep.Unchanged++ // already at its new key (the same under both folds, or filed since)
		case !okOld || err != nil || old != r.Key:
			rep.Foreign = append(rep.Foreign, r.Key)
		default:
			rep.Changes = append(rep.Changes, Change{Old: r.Key, New: nk})
		}
	}
	slices.SortFunc(rep.Changes, func(a, b Change) int { return strings.Compare(string(a.Old), string(b.Old)) })
	return rep
}

// collisions refuses a plan that would give two postings one key: two moves
// to one key, a move onto a posting that already exists, or a move onto a
// key the user has marks under.
func collisions(cs []Change, l ledger.Ledger, current map[posting.Key]marks.Entry, rs *resumestore.Store) error {
	var bad []string
	to := map[posting.Key]posting.Key{}
	for _, c := range cs {
		if prev, dup := to[c.New]; dup {
			bad = append(bad, fmt.Sprintf("%s and %s both become %s", prev, c.Old, c.New))
		}
		to[c.New] = c.Old
		if _, exists := l.Get(c.New); exists {
			bad = append(bad, fmt.Sprintf("%s would become %s, which is already in the ledger", c.Old, c.New))
		}
		if _, marked := current[c.New]; marked {
			bad = append(bad, fmt.Sprintf("%s would become %s, which already has marks", c.Old, c.New))
		}
		// The same condition resumestore.Rekey refuses on (a folder of any
		// content), checked here so the refusal comes before the ledger is touched.
		switch has, err := rs.Exists(string(c.New)); {
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s would become %s, whose resume folder cannot be checked: %v", c.Old, c.New, err))
		case has:
			bad = append(bad, fmt.Sprintf("%s would become %s, which already has a resume folder", c.Old, c.New))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("%w: %s", ErrCollision, strings.Join(bad, "; "))
	}
	return nil
}

// rewrite returns the events file with every event of a moved posting under
// its new key: the event key, the posting's own key, and a duplicate-of
// annotation that names it. Every other line is kept byte for byte.
func rewrite(src []byte, cs []Change) ([]byte, error) {
	moves := make(map[posting.Key]posting.Key, len(cs))
	for _, c := range cs {
		moves[c.Old] = c.New
	}
	if len(moves) == 0 {
		return src, nil
	}
	var out bytes.Buffer
	out.Grow(len(src))
	r := bufio.NewReader(bytes.NewReader(src))
	for n := 1; ; n++ {
		line, err := r.ReadBytes('\n')
		if len(line) == 0 {
			break
		}
		if err != nil || !mentionsMove(line, moves) {
			out.Write(line) // untouched, a final unterminated line included
			if err != nil {
				break
			}
			continue
		}
		var e ledger.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("rekey: line %d: %w", n, err)
		}
		changed := false
		if nk, ok := moves[e.Key]; ok {
			e.Key, changed = nk, true
			if e.Posting != nil {
				e.Posting.Key = nk
			}
		}
		if nk, ok := moves[posting.Key(e.Annotations[pipeline.DuplicateOfKey])]; ok {
			e.Annotations[pipeline.DuplicateOfKey], changed = string(nk), true
		}
		if !changed {
			out.Write(line) // the key appeared only as text elsewhere in the line
			continue
		}
		b, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("rekey: line %d: %w", n, err)
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

func mentionsMove(line []byte, moves map[posting.Key]posting.Key) bool {
	if !bytes.Contains(line, []byte(`"x:`)) {
		return false
	}
	for old := range moves {
		if bytes.Contains(line, []byte(old)) {
			return true
		}
	}
	return false
}

// moveMarks gives each moved posting's mark and note to its new key, at the
// times they were made, and then clears them under the old key. It is safe
// to repeat: a move already made is skipped, and a half-made one is finished.
func moveMarks(dir string, cs []Change, now time.Time) (int, error) {
	if len(cs) == 0 {
		return 0, nil
	}
	cur, err := marks.Read(dir)
	if err != nil {
		return 0, fmt.Errorf("rekey: marks: %w", err)
	}
	moved := 0
	for _, c := range cs {
		old, ok := cur[c.Old]
		if !ok {
			continue
		}
		have := cur[c.New]
		if (have.Mark != old.Mark || have.ResumeVersion != old.ResumeVersion) && old.Mark != marks.None {
			// An applied mark carries the resume version that was sent; it is a
			// record of what went out, so it moves with the mark.
			set := func() error {
				if old.Mark == marks.Applied {
					_, err := marks.SetApplied(dir, c.New, old.ResumeVersion, old.MarkedAt)
					return err
				}
				_, err := marks.SetMark(dir, c.New, old.Mark, old.MarkedAt)
				return err
			}
			if err := set(); err != nil {
				return moved, fmt.Errorf("rekey: move mark %s: %w", c.Old, err)
			}
		}
		// The new key had no marks (collisions refuses otherwise), so a stage
		// there while the old key has none is the move already made, the old
		// key's stage having been set back as the last step below.
		if old.Mark == marks.Applied && (have.Stage != old.Stage || have.Furthest != old.Furthest) && !(old.Stage == marks.StageNone && have.Stage != marks.StageNone) {
			// The stage history moves with the application: the furthest stage
			// first, then the current one. A line for each is enough, and a
			// crash between them is finished by the next run.
			for _, s := range []marks.Stage{old.Furthest, old.Stage} {
				if s == marks.StageNone && old.Stage != marks.StageNone {
					continue
				}
				if _, err := marks.SetStage(dir, c.New, s, old.StageAt); err != nil {
					return moved, fmt.Errorf("rekey: move stage %s: %w", c.Old, err)
				}
			}
		}
		if have.Note != old.Note && old.Note != "" {
			if _, err := marks.SetNote(dir, c.New, old.Note, old.NoteAt); err != nil {
				return moved, fmt.Errorf("rekey: move note %s: %w", c.Old, err)
			}
		}
		if old.Stage != marks.StageNone {
			// A posting at a stage leaves Applied only once the stage is set
			// back, so the old key's stage is set back before its mark is cleared.
			if _, err := marks.SetStage(dir, c.Old, marks.StageNone, now); err != nil {
				return moved, fmt.Errorf("rekey: clear stage %s: %w", c.Old, err)
			}
		}
		if old.Mark != marks.None {
			if _, err := marks.SetMark(dir, c.Old, marks.None, now); err != nil {
				return moved, fmt.Errorf("rekey: clear mark %s: %w", c.Old, err)
			}
		}
		if old.Note != "" {
			if _, err := marks.SetNote(dir, c.Old, "", now); err != nil {
				return moved, fmt.Errorf("rekey: clear note %s: %w", c.Old, err)
			}
		}
		moved++
		if err := crash("marks"); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

// moveResumes gives each moved posting's resume folder (its versions, current
// and used pointers and attempts) to its new key. It runs after the marks move,
// so an applied mark's resume version is found under the new key once both are
// done; between the two a crash leaves the mark at the new key and the folder
// at the old, and the next run finishes it. A posting with no resume folder is
// skipped; a folder already moved is left as it is, so the step can be
// repeated. It returns how many folders this call moved.
func moveResumes(rs *resumestore.Store, cs []Change) (int, error) {
	moved := 0
	for _, c := range cs {
		had, err := rs.Exists(string(c.Old))
		if err != nil {
			return moved, fmt.Errorf("rekey: resumes %s: %w", c.Old, err)
		}
		switch err := rs.Rekey(context.Background(), string(c.Old), string(c.New)); {
		case errors.Is(err, resumestore.ErrNotFound):
			continue
		case err != nil:
			return moved, fmt.Errorf("rekey: move resumes %s: %w", c.Old, err)
		}
		if had {
			moved++
		}
		if err := crash("resumes"); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

func readMarker(path string) (*marker, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rekey: read marker: %w", err)
	}
	var m marker
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || m.V != markerVersion || (m.State != "pending" && m.State != "done") {
		return nil, fmt.Errorf("rekey: marker %s is unreadable; nothing was done (%v)", path, err)
	}
	return &m, nil
}

func writeMarker(path string, m *marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("rekey: marker: %w", err)
	}
	if err := atomicfile.Write(path, append(b, '\n')); err != nil {
		return fmt.Errorf("rekey: marker: %w", err)
	}
	return nil
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fallbackV1 is posting.FallbackKey as it was before titles kept language
// punctuation: every field folded by the same rule. It is frozen here as the
// migration's definition of an old key.
func fallbackV1(company, title, location string) (posting.Key, bool) {
	c, t := foldV1(company), foldV1(title)
	if c == "" || t == "" {
		return "", false
	}
	s := sha256.Sum256([]byte(c + "|" + t + "|" + foldV1(location)))
	return posting.Key(string(posting.Other) + ":" + hex.EncodeToString(s[:])[:24]), true
}

func foldV1(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
			continue
		}
		space = true
	}
	return b.String()
}
