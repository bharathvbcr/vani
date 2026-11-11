package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// readerView is everything a Reader answers, for comparing two Readers.
type readerView struct {
	Snap   Snapshot
	Bodies map[posting.Key]string
	FPs    map[posting.Key][16]byte
}

func viewOf(t *testing.T, r *Reader) readerView {
	t.Helper()
	v := readerView{Snap: r.Snapshot(), Bodies: map[posting.Key]string{}, FPs: map[posting.Key][16]byte{}}
	for _, rec := range v.Snap.Records {
		if fp, ok := r.BodyFP(rec.Key); ok {
			v.FPs[rec.Key] = fp
		}
		if rec.BodyEvicted {
			d, err := r.Body(rec.Key)
			if err != nil {
				t.Fatalf("body %s: %v", rec.Key, err)
			}
			v.Bodies[rec.Key] = d
		}
	}
	return v
}

// freshView folds dir from scratch, no checkpoint.
func freshView(t *testing.T, dir string) readerView {
	t.Helper()
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	return viewOf(t, r)
}

// sameView compares by JSON, which is what every consumer of a record sees:
// a decoded checkpoint and a replayed line can differ in pointer identity of
// equal time zones, never in a field.
func sameView(t *testing.T, label string, got, want readerView) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(g) != string(w) {
		t.Fatalf("%s: reader differs from a fresh fold\n got: %.600s\nwant: %.600s", label, g, w)
	}
	for i := range got.Snap.Records {
		if got.Snap.Records[i].BodyEvicted != want.Snap.Records[i].BodyEvicted {
			t.Fatalf("%s: %s BodyEvicted %v, fresh %v", label, got.Snap.Records[i].Key, got.Snap.Records[i].BodyEvicted, want.Snap.Records[i].BodyEvicted)
		}
	}
}

// postEv is an event carrying a posting with body d.
func postEv(k string, st posting.Status, at time.Time, title, d string) Event {
	e := ev(k, st, at)
	e.Posting = &posting.Posting{Key: posting.Key(k), Company: "Acme", Title: title, Location: "Austin, TX", URL: "https://x/" + k, Description: d}
	e.Annotations = map[string]string{"triage.role_fit": "strong_fit", AnnRank: "7"}
	return e
}

// growLedger appends n events per round to the ledger at dir, mixing new
// postings, re-posted bodies, status moves and annotation changes on old keys.
func growLedger(t *testing.T, dir string, round, n int) {
	t.Helper()
	f := mustOpen(t, dir)
	defer f.Close()
	ctx := context.Background()
	for i := range n {
		k := fmt.Sprintf("gh:acme:%d-%d", round, i)
		if err := f.Append(ctx, postEv(k, posting.Triaged, t0.Add(time.Duration(round*100+i)*time.Minute), "Engineer", nastyBodies[i%len(nastyBodies)])); err != nil {
			t.Fatal(err)
		}
		if round > 0 && i%3 == 0 {
			old := fmt.Sprintf("gh:acme:%d-%d", round-1, i)
			e := ev(old, posting.Dead, t0.Add(time.Duration(round*100+i)*time.Minute+time.Second))
			e.Annotations = map[string]string{"note": fmt.Sprintf("round %d", round), AnnRank: ""}
			if err := f.Append(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		if round > 0 && i%4 == 1 {
			old := fmt.Sprintf("gh:acme:%d-%d", round-1, i)
			if err := f.Append(ctx, postEv(old, posting.Scored, t0.Add(time.Duration(round*100+i)*time.Minute+2*time.Second), "Staff Engineer", "reposted "+old)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func appendRaw(t *testing.T, dir, s string) {
	t.Helper()
	fh, err := os.OpenFile(eventsPath(dir), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func lineOf(t *testing.T, e Event) string {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// Advance after every batch of appends answers exactly what a fresh fold of
// the file does: records, bodies read back, and fingerprints.
func TestReaderAdvanceMatchesAFreshFold(t *testing.T) {
	dir := t.TempDir()
	growLedger(t, dir, 0, 12)
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sameView(t, "open", viewOf(t, r), freshView(t, dir))
	for round := 1; round <= 4; round++ {
		growLedger(t, dir, round, 9)
		changed, err := r.Advance()
		if err != nil || !changed {
			t.Fatalf("round %d: Advance changed=%v err=%v", round, changed, err)
		}
		if r.Info().Refolds != 0 {
			t.Fatalf("round %d: appends made a refold (%+v)", round, r.Info())
		}
		sameView(t, fmt.Sprintf("round %d", round), viewOf(t, r), freshView(t, dir))
	}
	if changed, err := r.Advance(); err != nil || changed {
		t.Fatalf("idle Advance changed=%v err=%v", changed, err)
	}
}

// A torn last line is left out and folded once it is whole.
func TestReaderTornTailIsReadOnceWhole(t *testing.T) {
	dir := t.TempDir()
	growLedger(t, dir, 0, 3)
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	line := lineOf(t, postEv("gh:acme:late", posting.Triaged, t0, "Late", "late body"))
	appendRaw(t, dir, line[:len(line)/2])
	if _, err := r.Advance(); err != nil {
		t.Fatal(err)
	}
	if v := viewOf(t, r); !v.Snap.Torn || len(v.Snap.Records) != 3 {
		t.Fatalf("torn half line: torn=%v records=%d", v.Snap.Torn, len(v.Snap.Records))
	}
	appendRaw(t, dir, line[len(line)/2:])
	if changed, err := r.Advance(); err != nil || !changed {
		t.Fatalf("completed line: changed=%v err=%v", changed, err)
	}
	got := viewOf(t, r)
	if got.Snap.Torn || len(got.Snap.Records) != 4 {
		t.Fatalf("after completion: torn=%v records=%d", got.Snap.Torn, len(got.Snap.Records))
	}
	sameView(t, "completed", got, freshView(t, dir))
}

// A writer that rolls a line back (File.Append truncates to its good size after
// a failed fsync) and then writes another one where it was: a size check alone
// misses the second case, where the file ends up longer than the reader's offset.
func TestReaderRefoldsAfterARolledBackLine(t *testing.T) {
	for _, longer := range []bool{false, true} {
		t.Run(fmt.Sprintf("then_longer=%v", longer), func(t *testing.T) {
			dir := t.TempDir()
			growLedger(t, dir, 0, 4)
			fi, _ := os.Stat(eventsPath(dir))
			good := fi.Size()
			r, err := OpenReader(dir, ReaderOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			appendRaw(t, dir, lineOf(t, postEv("gh:acme:phantom", posting.Triaged, t0, "Phantom", "rolled back")))
			if _, err := r.Advance(); err != nil {
				t.Fatal(err)
			}
			if err := os.Truncate(eventsPath(dir), good); err != nil {
				t.Fatal(err)
			}
			if longer {
				appendRaw(t, dir, lineOf(t, postEv("gh:acme:real-and-longer", posting.Triaged, t0, "The real one, longer", "kept body, longer than the phantom")))
			}
			changed, err := r.Advance()
			if err != nil || !changed {
				t.Fatalf("after rollback: changed=%v err=%v", changed, err)
			}
			if r.Info().Refolds != 1 {
				t.Fatalf("refolds %d, want 1", r.Info().Refolds)
			}
			got := viewOf(t, r)
			for _, rec := range got.Snap.Records {
				if rec.Key == "gh:acme:phantom" {
					t.Fatal("the rolled-back line is still folded")
				}
			}
			sameView(t, "after rollback", got, freshView(t, dir))
		})
	}
}

// rekey renames a new events file over the old one.
func TestReaderRefoldsAReplacedFile(t *testing.T) {
	dir := t.TempDir()
	growLedger(t, dir, 0, 5)
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	other := t.TempDir()
	growLedger(t, other, 0, 2)
	growLedger(t, other, 1, 7)
	if err := os.Rename(eventsPath(other), eventsPath(dir)); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.Advance(); err != nil || !changed {
		t.Fatalf("replaced: changed=%v err=%v", changed, err)
	}
	sameView(t, "replaced", viewOf(t, r), freshView(t, dir))
}

// Corruption in the appended part is ErrCorrupt; once it is gone the reader
// recovers by folding again rather than keeping a half-applied tail.
func TestReaderCorruptTailFailsLoudAndRecovers(t *testing.T) {
	dir := t.TempDir()
	growLedger(t, dir, 0, 3)
	fi, _ := os.Stat(eventsPath(dir))
	good := fi.Size()
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	appendRaw(t, dir, lineOf(t, postEv("gh:acme:ok", posting.Triaged, t0, "OK", "fine"))+`{"v":1,"key":"gh:acme:x","status":"bogus","at":"2026-10-01T12:00:00Z"}`+"\n"+lineOf(t, ev("gh:acme:ok", posting.Scored, t0)))
	if _, err := r.Advance(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt tail: %v, want ErrCorrupt", err)
	}
	if err := os.Truncate(eventsPath(dir), good); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(); err != nil {
		t.Fatal(err)
	}
	sameView(t, "recovered", viewOf(t, r), freshView(t, dir))
}

func ckOpts(dir string) ReaderOptions {
	return ReaderOptions{Checkpoint: filepath.Join(dir, "readmodel", "ledger.ckpt"), SaveAfter: 1}
}

// A checkpoint plus the tail appended after it folds to exactly what the whole
// file does, including annotation edits and re-posts of checkpointed records.
func TestReaderCheckpointThenTailMatchesAFreshFold(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "ledger")
	growLedger(t, dir, 0, 10)
	growLedger(t, dir, 1, 10)
	r, err := OpenReader(dir, ckOpts(state))
	if err != nil {
		t.Fatal(err)
	}
	if info := r.Info(); info.Checkpoint != "not used: no checkpoint yet" || info.Saved != "saved" {
		t.Fatalf("first open: %+v", info)
	}
	r.Close()
	if entries, _ := os.ReadDir(dir); len(entries) != 2 { // events.jsonl and the writer's lock
		t.Fatalf("the reader wrote into the ledger directory: %v", entries)
	}

	r, err = OpenReader(dir, ckOpts(state))
	if err != nil {
		t.Fatal(err)
	}
	if info := r.Info(); info.Checkpoint != "loaded" || info.Folded != 0 {
		t.Fatalf("second open: %+v", info)
	}
	sameView(t, "from checkpoint", viewOf(t, r), freshView(t, dir))
	r.Close()

	growLedger(t, dir, 2, 8)
	r, err = OpenReader(dir, ckOpts(state))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if info := r.Info(); info.Checkpoint != "loaded" || info.Folded == 0 {
		t.Fatalf("third open: %+v", info)
	}
	sameView(t, "checkpoint plus tail", viewOf(t, r), freshView(t, dir))
	growLedger(t, dir, 3, 5) // and an Advance on top of a checkpointed fold
	if _, err := r.Advance(); err != nil {
		t.Fatal(err)
	}
	sameView(t, "checkpoint plus tail plus advance", viewOf(t, r), freshView(t, dir))
}

// Every way a checkpoint can stop describing the file makes the reader fold
// from scratch, say why, and answer exactly as a fresh fold does.
func TestReaderRejectsACheckpointThatNoLongerFits(t *testing.T) {
	cases := []struct {
		name, why string
		mutate    func(t *testing.T, state, dir string)
	}{
		{"replaced", "another events file", func(t *testing.T, state, dir string) {
			other := t.TempDir()
			growLedger(t, other, 0, 6)
			if err := os.Rename(eventsPath(other), eventsPath(dir)); err != nil {
				t.Fatal(err)
			}
		}},
		{"shorter", "shorter than the checkpoint", func(t *testing.T, state, dir string) {
			fi, _ := os.Stat(eventsPath(dir))
			if err := os.Truncate(eventsPath(dir), fi.Size()/2); err != nil {
				t.Fatal(err)
			}
		}},
		{"last line rewritten", "last line it covers changed", func(t *testing.T, state, dir string) {
			raw, _ := os.ReadFile(eventsPath(dir))
			i := strings.LastIndex(strings.TrimSuffix(string(raw), "\n"), "\n") + 1
			last := strings.Replace(string(raw[i:]), "Austin, TX", "Austin, TZ", 1) // same length, still valid
			if last == string(raw[i:]) {
				t.Fatal("setup: nothing to rewrite")
			}
			if err := os.WriteFile(eventsPath(dir), append(raw[:i:i], last...), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"sampled line edited in place", "sampled line changed", func(t *testing.T, state, dir string) {
			raw, _ := os.ReadFile(eventsPath(dir))
			edited := strings.Replace(string(raw), "plain text", "PLAIN TEXT", -1)
			if edited == string(raw) {
				t.Fatal("setup: nothing to edit")
			}
			if err := os.WriteFile(eventsPath(dir), []byte(edited), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"garbled checkpoint", "unreadable", func(t *testing.T, state, dir string) {
			if err := os.WriteFile(ckOpts(state).Checkpoint, []byte("not a checkpoint"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			dir := filepath.Join(state, "ledger")
			growLedger(t, dir, 0, 10)
			r, err := OpenReader(dir, ckOpts(state))
			if err != nil {
				t.Fatal(err)
			}
			r.Close()
			c.mutate(t, state, dir)
			r, err = OpenReader(dir, ReaderOptions{Checkpoint: ckOpts(state).Checkpoint})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if info := r.Info(); !strings.Contains(info.Checkpoint, c.why) {
				t.Fatalf("checkpoint %q, want it rejected for %q", info.Checkpoint, c.why)
			}
			sameView(t, c.name, viewOf(t, r), freshView(t, dir))
		})
	}
}

// Two writers never interleave: one that finds the checkpoint lock held skips.
func TestReaderCheckpointSaveSkipsWhileAnotherWrites(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "ledger")
	growLedger(t, dir, 0, 4)
	lock, err := filelock.TryAcquire(ckOpts(state).Checkpoint + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(dir, ckOpts(state))
	if err != nil {
		t.Fatal(err)
	}
	if info := r.Info(); !strings.HasPrefix(info.Saved, "skipped") {
		t.Fatalf("saved %q while the lock was held", info.Saved)
	}
	lock.Release()
	if err := r.SaveCheckpoint(); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := os.Stat(ckOpts(state).Checkpoint); err != nil {
		t.Fatal(err)
	}
}

// A reader without a checkpoint writes nothing anywhere, and a missing ledger
// is an error that creates nothing.
func TestReaderIsReadOnlyWithoutACheckpoint(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "ledger")
	growLedger(t, dir, 0, 3)
	before, _ := os.ReadDir(state)
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r.Advance()
	r.Close()
	if after, _ := os.ReadDir(state); len(after) != len(before) {
		t.Fatalf("state dir changed: %v -> %v", before, after)
	}
	missing := filepath.Join(state, "nope")
	if _, err := OpenReader(missing, ckOpts(state)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing ledger: %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a reader created the ledger directory")
	}
}

// Readers and an appending writer at once: every answer is consistent and the
// final one matches a fresh fold. Run with -race.
func TestReaderConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	growLedger(t, dir, 0, 5)
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := r.Advance(); err != nil {
					t.Error(err)
					return
				}
				snap := r.Snapshot()
				for _, rec := range snap.Records {
					if rec.BodyEvicted {
						if _, err := r.Body(rec.Key); err != nil {
							t.Errorf("body %s: %v", rec.Key, err)
							return
						}
					}
				}
			}
		})
	}
	for round := 1; round <= 3; round++ {
		growLedger(t, dir, round, 6)
	}
	close(stop)
	wg.Wait()
	if _, err := r.Advance(); err != nil {
		t.Fatal(err)
	}
	sameView(t, "after concurrent use", viewOf(t, r), freshView(t, dir))
}

// Closed readers refuse rather than answer from a stale fold.
func TestReaderClosed(t *testing.T) {
	dir := t.TempDir()
	growLedger(t, dir, 0, 2)
	r, err := OpenReader(dir, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Advance(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Advance after Close: %v", err)
	}
	if _, err := r.Body("gh:acme:0-0"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Body after Close: %v", err)
	}
}

// The fingerprint is the writer's encoding of the text: the same for a body
// read whole and one cut out of its line.
func TestBodyFingerprintAgreesAcrossDecodes(t *testing.T) {
	for _, d := range nastyBodies {
		line := lineOf(t, postEv("gh:acme:fp", posting.Triaged, t0, "T", d))
		_, start, end, ok := stripDescriptionSpan([]byte(strings.TrimSuffix(line, "\n")), nil)
		if !ok {
			t.Fatalf("%q: not cut", d)
		}
		if got, want := BodyFingerprint([]byte(line[start:end])), bodyFingerprintOf(d); got != want {
			t.Errorf("%q: cut %x, decoded %x", d, got, want)
		}
	}
}
