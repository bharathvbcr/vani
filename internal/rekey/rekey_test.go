//go:build !windows

package rekey

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var (
	t0  = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	run = t0.Add(24 * time.Hour)
)

// oldKey is the key a posting got before titles kept language punctuation.
func oldKey(t *testing.T, p posting.Posting) posting.Key {
	t.Helper()
	k, ok := fallbackV1(p.Company, p.Title, p.Location)
	if !ok {
		t.Fatalf("no v1 key for %+v", p)
	}
	return k
}

func newKey(t *testing.T, p posting.Posting) posting.Key {
	t.Helper()
	k, err := posting.FallbackKey(p.Company, p.Title, p.Location)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// appendAll writes events through the real ledger.
func appendAll(t *testing.T, state string, es ...ledger.Event) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, e := range es {
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

// stored files a posting under key k, as an adapter that stored fallback
// keys would: new, then triaged.
func stored(k posting.Key, p posting.Posting) []ledger.Event {
	p.Key = k
	return []ledger.Event{
		{V: ledger.SchemaVersion, At: t0, Key: k, Status: posting.Live, Posting: &p, Reason: "new"},
		{V: ledger.SchemaVersion, At: t0.Add(time.Hour), Key: k, Status: posting.Triaged, Annotations: map[string]string{"triage.remote": "remote"}, Reason: "triaged"},
	}
}

func events(t *testing.T, state string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "ledger", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	cpp   = posting.Posting{Company: "Acme", Title: "Senior C++ Engineer", Location: "Remote", Description: "d"}
	plain = posting.Posting{Company: "Acme", Title: "Senior Platform Engineer", Location: "Remote", Description: "d"}
)

func ghPosting(t *testing.T, id string) posting.Posting {
	k, err := posting.GreenhouseKey("acme", id)
	if err != nil {
		t.Fatal(err)
	}
	return posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "Senior C++ Engineer " + id, Location: "Remote", Description: "d"}
}

// An x: posting whose title holds C++ moves to its new key with all its
// events, its duplicate-of references and its marks; the gh posting beside
// it, and the x: posting whose key does not change, are byte for byte as
// they were.
func TestFallbackPostingMovesAndNothingElseChanges(t *testing.T) {
	state := t.TempDir()
	gh := ghPosting(t, "1")
	ok, nk, keep := oldKey(t, cpp), newKey(t, cpp), oldKey(t, plain)
	if ok == nk || keep != newKey(t, plain) {
		t.Fatal("fixture: the C++ title must change key and the plain one must not")
	}
	appendAll(t, state, stored(ok, cpp)...)
	appendAll(t, state, stored(keep, plain)...)
	appendAll(t, state, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: gh.Key, Status: posting.Live, Posting: &gh, Reason: "new"})
	// A weak twin recorded as a duplicate of the moving posting.
	twin := plain
	twin.Title = "Platform Engineer twin"
	tk := oldKey(t, twin)
	appendAll(t, state, stored(tk, twin)[0], ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Hour), Key: tk, Status: posting.Duplicate,
		Annotations: map[string]string{pipeline.DuplicateOfKey: string(ok)}, Reason: "duplicate of " + string(ok)})
	md := marks.Dir(state)
	if _, err := marks.SetApplied(md, ok, 3, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetNote(md, ok, "called back", t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := events(t, state)

	rep, err := Run(state, false, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 || rep.Changes[0] != (Change{Old: ok, New: nk}) || rep.Unchanged != 2 || rep.MarksMoved != 1 {
		t.Fatalf("report %+v", rep)
	}

	snap, err := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[posting.Key]ledger.Record{}
	for _, r := range snap.Records {
		got[r.Key] = r
	}
	if _, still := got[ok]; still {
		t.Fatal("the old key is still in the ledger")
	}
	moved, has := got[nk]
	if !has || moved.Status != posting.Triaged || moved.Posting.Key != nk || moved.Annotations["triage.remote"] != "remote" || !moved.FirstSeen.Equal(t0) {
		t.Fatalf("moved record %+v", moved)
	}
	if got[tk].Annotations[pipeline.DuplicateOfKey] != string(nk) {
		t.Fatalf("duplicate-of still names the old key: %v", got[tk].Annotations)
	}

	// Every line that does not concern the moved posting is unchanged.
	after := events(t, state)
	bl, al := bytes.Split(before, []byte("\n")), bytes.Split(after, []byte("\n"))
	if len(bl) != len(al) {
		t.Fatalf("%d lines became %d", len(bl), len(al))
	}
	for i := range bl {
		if bytes.Contains(bl[i], []byte(ok)) {
			continue
		}
		if !bytes.Equal(bl[i], al[i]) {
			t.Fatalf("line %d changed though it does not concern the move:\n%s\n%s", i+1, bl[i], al[i])
		}
	}

	m, err := marks.Read(md)
	if err != nil {
		t.Fatal(err)
	}
	if _, still := m[ok]; still || m[nk].Mark != marks.Applied || m[nk].ResumeVersion != 3 || m[nk].Note != "called back" || !m[nk].MarkedAt.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("marks %+v", m)
	}

	// The backup is the file as it was, and the marker lists the move.
	if b, _ := os.ReadFile(rep.Backup); !bytes.Equal(b, before) {
		t.Fatal("backup is not the file as it was")
	}
	mk, err := readMarker(filepath.Join(state, "ledger", MarkerName))
	if err != nil || mk.State != "done" || len(mk.Changes) != 1 || mk.Changes[0].Old != ok {
		t.Fatalf("marker %+v %v", mk, err)
	}
}

// Without the migration the posting keeps a key FallbackKey no longer gives
// it, so the ledger and the code disagree about its identity.
func TestWithoutTheMigrationTheKeyIsStale(t *testing.T) {
	state := t.TempDir()
	appendAll(t, state, stored(oldKey(t, cpp), cpp)...)
	snap, _ := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if r := snap.Records[0]; r.Key == newKey(t, r.Posting) {
		t.Fatal("fixture: the stored key already matches")
	}
	if _, err := Run(state, false, run); err != nil {
		t.Fatal(err)
	}
	snap, _ = ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	if r := snap.Records[0]; r.Key != newKey(t, r.Posting) {
		t.Fatalf("after the migration the key is still %s", r.Key)
	}
}

// Today's ledgers hold no x: postings: the run changes no byte of the events
// file and says so.
func TestLedgerWithoutFallbackKeysIsByteIdentical(t *testing.T) {
	state := t.TempDir()
	for _, id := range []string{"1", "2", "3"} {
		p := ghPosting(t, id)
		appendAll(t, state, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: p.Key, Status: posting.Live, Posting: &p, Reason: "new"})
	}
	before := events(t, state)
	rep, err := Run(state, false, run)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fallback != 0 || len(rep.Changes) != 0 || rep.Records != 3 {
		t.Fatalf("report %+v", rep)
	}
	if !bytes.Equal(events(t, state), before) {
		t.Fatal("a ledger with no fallback keys changed")
	}
}

func TestEmptyLedger(t *testing.T) {
	state := t.TempDir()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	rep, err := Run(state, false, run)
	if err != nil || rep.Records != 0 || len(rep.Changes) != 0 {
		t.Fatalf("report %+v, %v", rep, err)
	}
	if len(events(t, state)) != 0 {
		t.Fatal("an empty ledger gained bytes")
	}
}

func TestNoLedgerIsAnError(t *testing.T) {
	if _, err := Run(t.TempDir(), true, run); err == nil {
		t.Fatal("a state dir without a ledger was migrated")
	}
}

func TestSecondRunIsRefused(t *testing.T) {
	state := t.TempDir()
	appendAll(t, state, stored(oldKey(t, cpp), cpp)...)
	if _, err := Run(state, false, run); err != nil {
		t.Fatal(err)
	}
	after := events(t, state)
	if _, err := Run(state, false, run.Add(time.Hour)); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("second run: %v", err)
	}
	if _, err := Run(state, true, run.Add(time.Hour)); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("second dry run: %v", err)
	}
	if !bytes.Equal(events(t, state), after) {
		t.Fatal("a refused run changed the ledger")
	}
}

// Two postings that the old fold kept apart and the new one does not (or a
// move onto a key already in use) are never merged: the run refuses, names
// them, and writes nothing.
func TestCollisionIsRefusedAndNamed(t *testing.T) {
	state := t.TempDir()
	ok, nk := oldKey(t, cpp), newKey(t, cpp)
	appendAll(t, state, stored(ok, cpp)...)
	appendAll(t, state, stored(nk, cpp)...) // the same posting, already filed under its new key
	before := events(t, state)
	_, err := Run(state, false, run)
	if !errors.Is(err, ErrCollision) || !strings.Contains(err.Error(), string(ok)) || !strings.Contains(err.Error(), string(nk)) {
		t.Fatalf("err = %v, want ErrCollision naming %s and %s", err, ok, nk)
	}
	if !bytes.Equal(events(t, state), before) {
		t.Fatal("a refused run changed the ledger")
	}
	if ents, _ := os.ReadDir(filepath.Join(state, "ledger")); len(ents) != 2 {
		t.Fatalf("a refused run left files: %v", ents)
	}
}

// A move onto a key the user already has marks under is refused too.
func TestCollisionWithMarksIsRefused(t *testing.T) {
	state := t.TempDir()
	ok, nk := oldKey(t, cpp), newKey(t, cpp)
	appendAll(t, state, stored(ok, cpp)...)
	if _, err := marks.SetMark(marks.Dir(state), nk, marks.Saved, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(state, false, run); !errors.Is(err, ErrCollision) {
		t.Fatalf("err = %v, want ErrCollision", err)
	}
}

// A ledger where some fallback postings already carry new keys (filed after
// the code changed) and others old ones: only the old ones move.
func TestPartlyMigratedLedger(t *testing.T) {
	state := t.TempDir()
	csharp := cpp
	csharp.Title = "Senior C# Engineer"
	appendAll(t, state, stored(newKey(t, csharp), csharp)...) // already new
	appendAll(t, state, stored(oldKey(t, cpp), cpp)...)       // still old
	rep, err := Run(state, false, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Old != oldKey(t, cpp) || rep.Unchanged != 1 || len(rep.Foreign) != 0 {
		t.Fatalf("report %+v", rep)
	}
	snap, _ := ledger.ReadSnapshot(filepath.Join(state, "ledger"))
	for _, r := range snap.Records {
		if r.Key != newKey(t, r.Posting) {
			t.Fatalf("%s is not at its new key %s", r.Key, newKey(t, r.Posting))
		}
	}
}

// A key that is not the old fold of its own fields was not made by
// FallbackKey (or its fields changed since): it is left where it is.
func TestForeignFallbackKeyIsLeftAlone(t *testing.T) {
	state := t.TempDir()
	appendAll(t, state, stored("x:0123456789abcdef01234567", cpp)...)
	before := events(t, state)
	rep, err := Run(state, false, run)
	if err != nil || len(rep.Foreign) != 1 || len(rep.Changes) != 0 {
		t.Fatalf("report %+v, %v", rep, err)
	}
	if !bytes.Equal(events(t, state), before) {
		t.Fatal("a foreign key was rewritten")
	}
}

// A crash at each step is finished by the next run, which ends exactly
// where an uninterrupted run would.
func TestCrashMidMigrationIsFinishedByTheNextRun(t *testing.T) {
	want := func() ([]byte, map[posting.Key]marks.Entry) {
		state := t.TempDir()
		seed(t, state)
		if _, err := Run(state, false, run); err != nil {
			t.Fatal(err)
		}
		m, _ := marks.Read(marks.Dir(state))
		return events(t, state), m
	}
	wantEvents, wantMarks := want()
	for _, step := range []string{"backup", "pending", "ledger", "marks"} {
		t.Run(step, func(t *testing.T) {
			state := t.TempDir()
			seed(t, state)
			boom := errors.New("crash at " + step)
			crash = func(s string) error {
				if s == step {
					return boom
				}
				return nil
			}
			_, err := Run(state, false, run)
			crash = func(string) error { return nil }
			if !errors.Is(err, boom) {
				t.Fatalf("first run: %v", err)
			}
			if _, err := ledger.ReadSnapshot(filepath.Join(state, "ledger")); err != nil {
				t.Fatalf("the ledger does not read after the crash: %v", err)
			}
			rep, err := Run(state, false, run)
			if step == "backup" {
				// No marker was written: the next run starts over, and its
				// backup name is taken by the first run's.
				if err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Fatalf("rerun after a crash before the marker: %v", err)
				}
				rep, err = Run(state, false, run.Add(time.Second))
			}
			if err != nil {
				t.Fatalf("rerun: %v", err)
			}
			if step != "backup" && !rep.Resumed {
				t.Fatal("the rerun did not resume")
			}
			if !bytes.Equal(events(t, state), wantEvents) {
				t.Fatal("the resumed ledger differs from an uninterrupted run")
			}
			m, _ := marks.Read(marks.Dir(state))
			if len(m) != len(wantMarks) {
				t.Fatalf("marks %v, want %v", m, wantMarks)
			}
			for k, e := range wantMarks {
				if g := m[k]; g.Mark != e.Mark || g.Note != e.Note {
					t.Fatalf("marks[%s] = %+v, want %+v", k, g, e)
				}
			}
			if _, err := Run(state, false, run.Add(time.Hour)); !errors.Is(err, ErrAlreadyRun) {
				t.Fatalf("a third run: %v", err)
			}
		})
	}
}

// A run that died and was followed by more appends cannot tell what to do:
// it refuses rather than guess.
func TestInterruptedRunOnAChangedLedgerIsRefused(t *testing.T) {
	state := t.TempDir()
	seed(t, state)
	crash = func(s string) error {
		if s == "pending" {
			return errors.New("crash")
		}
		return nil
	}
	Run(state, false, run)
	crash = func(string) error { return nil }
	p := ghPosting(t, "9")
	appendAll(t, state, ledger.Event{V: ledger.SchemaVersion, At: run, Key: p.Key, Status: posting.Live, Posting: &p, Reason: "new"})
	if _, err := Run(state, false, run); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v, want ErrInterrupted", err)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	state := t.TempDir()
	seed(t, state)
	before := events(t, state)
	rep, err := Run(state, true, run)
	if err != nil || len(rep.Changes) != 2 || !rep.DryRun || rep.Backup != "" {
		t.Fatalf("report %+v, %v", rep, err)
	}
	if !bytes.Equal(events(t, state), before) {
		t.Fatal("a dry run changed the ledger")
	}
	if ents, _ := os.ReadDir(filepath.Join(state, "ledger")); len(ents) != 2 {
		t.Fatalf("a dry run left files: %v", ents)
	}
}

// seed is two moving x: postings with marks, a gh posting and a steady x: one.
// The C# role is at another company: at Acme it would share the C++ role's
// old key (the old fold read both as "senior c engineer"), which is the bug
// the new fold fixes, and the old ledger would hold them as one posting.
func seed(t *testing.T, state string) {
	t.Helper()
	csharp := cpp
	csharp.Company, csharp.Title = "Globex", "Senior C# Engineer"
	appendAll(t, state, stored(oldKey(t, cpp), cpp)...)
	appendAll(t, state, stored(oldKey(t, csharp), csharp)...)
	appendAll(t, state, stored(oldKey(t, plain), plain)...)
	gh := ghPosting(t, "1")
	appendAll(t, state, ledger.Event{V: ledger.SchemaVersion, At: t0, Key: gh.Key, Status: posting.Live, Posting: &gh, Reason: "new"})
	md := marks.Dir(state)
	marks.SetMark(md, oldKey(t, cpp), marks.Saved, t0)
	marks.SetNote(md, oldKey(t, csharp), "ask about the stack", t0)
	marks.SetMark(md, gh.Key, marks.Hidden, t0)
}
