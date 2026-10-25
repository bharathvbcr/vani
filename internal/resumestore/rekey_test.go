package resumestore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// noPDFMeta is a record for a version saved without a PDF (a render that was
// not fitted).
func noPDFMeta() Meta {
	m := goodMeta()
	m.Checks.Fit, m.Pages = "not_run", 0
	return m
}

// seeded saves three versions under key (the second with no PDF), moves current
// to v2 and binds v3 as used, and returns the saved records.
func seeded(t *testing.T, s *Store, key string) []Meta {
	t.Helper()
	var out []Meta
	for _, v := range []struct {
		m   Meta
		pdf []byte
	}{{goodMeta(), pdfBytes("one")}, {noPDFMeta(), nil}, {goodMeta(), pdfBytes("three")}} {
		m, _ := mustSave(t, s, key, v.m, v.pdf)
		out = append(out, m)
	}
	if err := s.SetCurrent(context.Background(), key, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Use(context.Background(), key, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAttempt(context.Background(), key, Attempt{Cost: Cost{USD: 0.1, Complete: true, Turns: 1}, Code: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRekeyMovesVersionsPointersAndAttempts(t *testing.T) {
	s, state := newStore(t)
	saved := seeded(t, s, "x:old")
	// Age the records so a rewrite that stamps "now" is seen.
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	for n := 1; n <= 3; n++ {
		if err := os.Chtimes(versionFile(filepath.Join(state, "resumes", jobFolder("x:old")), n, "json"), old, old); err != nil {
			t.Fatal(err)
		}
	}
	_, wantPDF, err := s.Get("x:old", 3)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
		t.Fatalf("Rekey: %v", err)
	}

	if _, err := s.List("x:old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old key still reads: %v", err)
	}
	ms, err := s.List("x:new")
	if err != nil || len(ms) != 3 {
		t.Fatalf("List(new) = %d versions, %v", len(ms), err)
	}
	for i, m := range ms {
		if m.Key != "x:new" || m.Version != saved[i].Version || !m.Created.Equal(saved[i].Created) || m.PDFSHA256 != saved[i].PDFSHA256 {
			t.Errorf("v%d = %+v, want the saved record under the new key", i+1, m)
		}
	}
	if cur, err := s.Current("x:new"); err != nil || cur.Version != 2 {
		t.Fatalf("current = %+v, %v", cur, err)
	}
	if used, err := s.Used("x:new"); err != nil || used.Version != 3 {
		t.Fatalf("used = %+v, %v", used, err)
	}
	if _, pdf, err := s.Get("x:new", 3); err != nil || !bytes.Equal(pdf, wantPDF) {
		t.Fatalf("the PDF did not survive byte for byte: %v", err)
	}
	if as, err := s.Attempts("x:new"); err != nil || len(as) != 1 {
		t.Fatalf("attempts = %v, %v", as, err)
	}
	sum, err := s.Summary("x:new")
	if err != nil || sum.Versions != 3 || sum.Current == nil || sum.Used == nil {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
	// Pruning orders jobs by their records' times: a re-key does not refresh them.
	for n := 1; n <= 3; n++ {
		st, err := os.Stat(versionFile(filepath.Join(state, "resumes", jobFolder("x:new")), n, "json"))
		if err != nil || !st.ModTime().Equal(old) {
			t.Errorf("v%d mtime = %v, want %v (%v)", n, st.ModTime(), old, err)
		}
	}
	// The next Save under the new key continues the numbering.
	if got, _ := mustSave(t, s, "x:new", noPDFMeta(), nil); got.Version != 4 {
		t.Fatalf("next version %d", got.Version)
	}
}

func TestRekeyRefusesToMergeAndChangesNothing(t *testing.T) {
	s, state := newStore(t)
	seeded(t, s, "x:old")
	mustSave(t, s, "x:new", goodMeta(), pdfBytes("theirs"))
	before := snapshotTree(t, filepath.Join(state, "resumes"))
	err := s.Rekey(context.Background(), "x:old", "x:new")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
		t.Fatalf("a refused rekey changed the store:\n%s\n---\n%s", before, after)
	}
	if ms, err := s.List("x:old"); err != nil || len(ms) != 3 {
		t.Fatalf("old job: %d %v", len(ms), err)
	}
	if ms, err := s.List("x:new"); err != nil || len(ms) != 1 {
		t.Fatalf("new job: %d %v", len(ms), err)
	}
}

// A folder that only holds an attempts log is a job folder too: Exists says so,
// and Rekey onto it refuses, so the caller's check and Rekey agree.
func TestRekeyOntoAnAttemptsOnlyFolderIsRefused(t *testing.T) {
	s, _ := newStore(t)
	seeded(t, s, "x:old")
	if err := s.AppendAttempt(context.Background(), "x:new", Attempt{Code: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if sum, err := s.Summary("x:new"); err != nil || sum.Versions != 0 {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
	if ok, err := s.Exists("x:new"); err != nil || !ok {
		t.Fatalf("Exists = %v, %v", ok, err)
	}
	if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
}

func TestRekeyIsIdempotentAndReportsNothingToMove(t *testing.T) {
	s, state := newStore(t)
	seeded(t, s, "x:old")
	if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(state, "resumes"))
	if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
		t.Fatalf("second Rekey: %v", err)
	}
	if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
		t.Fatal("a repeated rekey changed the store")
	}
	// Neither key has a job: nothing to move.
	if err := s.Rekey(context.Background(), "x:nope", "x:nada"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRekeyRejectsBadArgumentsAndAnUnwrittenStore(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	if err := s.Rekey(ctx, "x:a", "x:a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("same key: %v", err)
	}
	if err := s.Rekey(ctx, "", "x:a"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty old key: %v", err)
	}
	if err := s.Rekey(ctx, "x:a", "bad\x00key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("control character: %v", err)
	}
	if err := s.Rekey(ctx, "x:a", "x:b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a store never written: %v", err)
	}
	if _, err := os.Stat(s.root); !os.IsNotExist(err) {
		t.Fatalf("Rekey created the store: %v", err)
	}
}

// A job whose records are not what the store wrote is refused before anything
// moves, so a refusal changes nothing and the old key still reads (as damaged).
func TestRekeyRefusesADamagedJobBeforeMovingIt(t *testing.T) {
	s, state := newStore(t)
	seeded(t, s, "x:old")
	path := versionFile(filepath.Join(state, "resumes", jobFolder("x:old")), 2, "json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Replace(raw, []byte(`"key":"x:old"`), []byte(`"key":"x:other"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, filepath.Join(state, "resumes"))
	if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrDamaged) {
		t.Fatalf("err = %v, want ErrDamaged", err)
	}
	if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
		t.Fatal("a refused rekey changed the store")
	}
}

// A folder under the new key's hash whose key file names some third key is not
// ours to finish and not ours to overwrite.
func TestRekeyRefusesAFolderThatBelongsToAnotherKey(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "x:elsewhere", goodMeta(), pdfBytes("1"))
	root := filepath.Join(state, "resumes")
	if err := os.Rename(filepath.Join(root, jobFolder("x:elsewhere")), filepath.Join(root, jobFolder("x:new"))); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, root)
	if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if after := snapshotTree(t, root); after != before {
		t.Fatal("a refused rekey changed the store")
	}
}

// A crash at each durable step leaves a state that reads honestly (never
// another job's data, never a silent partial answer) and that Rekey run again
// finishes.
func TestRekeyCrashAtEachStepIsReadableAndResumable(t *testing.T) {
	for _, stage := range []string{"rename", "key", "v1", "v2"} {
		t.Run(stage, func(t *testing.T) {
			s, state := newStore(t)
			seeded(t, s, "x:old")
			boom := errors.New("crash after " + stage)
			afterStage = func(st string) error {
				if st == stage {
					return boom
				}
				return nil
			}
			err := s.Rekey(context.Background(), "x:old", "x:new")
			afterStage = func(string) error { return nil }
			if !errors.Is(err, boom) {
				t.Fatalf("the crash was swallowed: %v", err)
			}

			// The old key no longer answers with a job; the new key is never read as
			// the old job without saying the folder is half moved.
			if _, err := s.List("x:old"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("List(old) = %v, want ErrNotFound", err)
			}
			ms, lerr := s.List("x:new")
			switch stage {
			case "rename":
				if !errors.Is(lerr, ErrDamaged) || len(ms) != 0 {
					t.Fatalf("List(new) after the rename = %d versions, %v; want ErrDamaged", len(ms), lerr)
				}
			case "key":
				if !errors.Is(lerr, ErrDamaged) || len(ms) != 0 {
					t.Fatalf("List(new) with no record rewritten = %d versions, %v; want only damage", len(ms), lerr)
				}
			default:
				if !errors.Is(lerr, ErrDamaged) || len(ms) == 0 || len(ms) == 3 {
					t.Fatalf("List(new) mid-records = %d versions, %v; want some versions and an ErrDamaged", len(ms), lerr)
				}
				for _, m := range ms {
					if m.Key != "x:new" {
						t.Fatalf("a version of the old key was served under the new one: %+v", m)
					}
				}
			}
			if _, err := s.Used("x:new"); stage == "rename" && !errors.Is(err, ErrDamaged) {
				t.Fatalf("Used(new) after the rename = %v, want ErrDamaged", err)
			}

			if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
				t.Fatalf("resuming: %v", err)
			}
			ms, err = s.List("x:new")
			if err != nil || len(ms) != 3 {
				t.Fatalf("after resuming: %d versions, %v", len(ms), err)
			}
			if used, err := s.Used("x:new"); err != nil || used.Version != 3 {
				t.Fatalf("used = %+v, %v", used, err)
			}
			if cur, err := s.Current("x:new"); err != nil || cur.Version != 2 {
				t.Fatalf("current = %+v, %v", cur, err)
			}
			if _, _, err := s.Get("x:new", 3); err != nil {
				t.Fatalf("Get after resuming: %v", err)
			}
			// And once more is a no-op.
			before := snapshotTree(t, filepath.Join(state, "resumes"))
			if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
				t.Fatalf("third Rekey: %v", err)
			}
			if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
				t.Fatal("the finished move changed again")
			}
		})
	}
}

// A Rekey waits for a Save in flight on the old key (its job lock) and honours
// its context.
func TestRekeyWaitsForTheJobLock(t *testing.T) {
	s, _ := newStore(t)
	seeded(t, s, "x:old")
	dir, release, err := s.lockJob(context.Background(), "x:old", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = dir
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := s.Rekey(ctx, "x:old", "x:new"); !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy while the job is locked", err)
	}
	release()
	if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
		t.Fatalf("after the lock is released: %v", err)
	}
}

// snapshotTree lists every file under root with its size and mtime, so a test
// can show a call changed nothing.
func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var b bytes.Buffer
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if filepath.Base(p) == lockFile {
			return nil // taking a lock may create the file
		}
		rel, _ := filepath.Rel(root, p)
		b.WriteString(rel + " " + info.Mode().String() + " " + info.ModTime().UTC().Format(time.RFC3339Nano))
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b.WriteString(" " + string(raw))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
