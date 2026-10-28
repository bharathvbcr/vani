package resumestore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// rekeyWithin fails the test when Rekey does not return inside bound. The PDF
// checks use it so a FIFO opened for read cannot sit until the package timeout.
func rekeyWithin(t *testing.T, s *Store, oldKey, newKey string, bound time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- s.Rekey(context.Background(), oldKey, newKey)
	}()
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case err := <-done:
		if time.Since(start) >= bound {
			t.Fatalf("Rekey took %s, want under %s", time.Since(start), bound)
		}
		return err
	case <-timer.C:
		t.Fatalf("Rekey did not return within %s", bound)
	}
	return nil
}

func assertNewKeyAbsent(t *testing.T, s *Store, oldKey, newKey string) {
	t.Helper()
	if _, err := os.Lstat(s.dir(oldKey)); err != nil {
		t.Fatalf("old folder: %v", err)
	}
	if _, err := os.Lstat(s.dir(newKey)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new folder = %v, want absent", err)
	}
}

// A valid newer version must not pull a damaged older one across the rename.
func TestRekeyLeavesEveryVersionWhenOnlyTheOlderIsDamaged(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
	mustSave(t, s, "x:old", goodMeta(), pdfBytes("two"))
	dir := s.dir("x:old")
	editRecord(t, dir, 1, func(raw []byte) []byte {
		out := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":9`), 1)
		if bytes.Equal(out, raw) {
			t.Fatal("version was not in the older record")
		}
		return out
	})
	before := snapshotTree(t, filepath.Join(state, "resumes"))
	older := mustRead(t, versionFile(dir, 1, "json"))
	newer := mustRead(t, versionFile(dir, 2, "json"))
	pdf := mustRead(t, versionFile(dir, 2, "pdf"))

	if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrDamaged) {
		t.Fatalf("err = %v, want ErrDamaged", err)
	}
	assertNewKeyAbsent(t, s, "x:old", "x:new")
	if got := mustRead(t, versionFile(dir, 1, "json")); !bytes.Equal(got, older) {
		t.Fatal("damaged older record was rewritten")
	}
	if got := mustRead(t, versionFile(dir, 2, "json")); !bytes.Equal(got, newer) {
		t.Fatal("healthy newer record was rewritten")
	}
	if got := mustRead(t, versionFile(dir, 2, "pdf")); !bytes.Equal(got, pdf) {
		t.Fatal("newer PDF bytes changed")
	}
	if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
		t.Fatalf("a refused rekey changed the store:\n%s\n---\n%s", before, after)
	}
}

// readMeta rejects parent >= n. checkMeta allows parent 0..MaxVersion.
// Version files are v1..v9999, so there is no v0 record. v1 can express both
// sides of the boundary: parent 1 equals n, parent 0 is n-1 (none). v2 can
// express parent 2 (equal, refused) and parent 1 (one less, kept).
func TestRekeyParentBoundaryFollowsReadMeta(t *testing.T) {
	t.Run("v1 parent equal to n", func(t *testing.T) {
		s, state := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
		editRecord(t, s.dir("x:old"), 1, func(raw []byte) []byte {
			out := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"parent":1`), 1)
			if bytes.Equal(out, raw) {
				t.Fatal("version was not in the record")
			}
			return out
		})
		before := snapshotTree(t, filepath.Join(state, "resumes"))
		if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("err = %v, want ErrDamaged", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
			t.Fatal("parent equal to n was moved")
		}
	})

	t.Run("v1 parent one less than n", func(t *testing.T) {
		s, _ := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
		editRecord(t, s.dir("x:old"), 1, func(raw []byte) []byte {
			out := bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"parent":0`), 1)
			if bytes.Equal(out, raw) {
				t.Fatal("version was not in the record")
			}
			return out
		})
		if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
			t.Fatalf("parent 0 on v1 is legal: %v", err)
		}
		got, pdf, err := s.Get("x:new", 1)
		if err != nil {
			t.Fatal(err)
		}
		if got.Key != "x:new" || got.Version != 1 || got.Parent != 0 || !bytes.Equal(pdf, pdfBytes("one")) {
			t.Fatalf("got key=%q version=%d parent=%d pdf=%q", got.Key, got.Version, got.Parent, pdf)
		}
		if _, err := os.Lstat(s.dir("x:old")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old folder: %v", err)
		}
	})

	t.Run("v2 parent equal to n", func(t *testing.T) {
		s, state := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
		mustSave(t, s, "x:old", goodMeta(), pdfBytes("two"))
		editRecord(t, s.dir("x:old"), 2, func(raw []byte) []byte {
			out := bytes.Replace(raw, []byte(`"version":2`), []byte(`"version":2,"parent":2`), 1)
			if bytes.Equal(out, raw) {
				t.Fatal("version was not in v2")
			}
			return out
		})
		before := snapshotTree(t, filepath.Join(state, "resumes"))
		if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("err = %v, want ErrDamaged", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
			t.Fatal("parent equal to n was moved")
		}
	})

	t.Run("v2 parent one less than n", func(t *testing.T) {
		s, _ := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
		next := goodMeta()
		next.Parent = 1
		mustSave(t, s, "x:old", next, pdfBytes("two"))
		if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
			t.Fatalf("parent 1 on v2 is legal: %v", err)
		}
		got, pdf, err := s.Get("x:new", 2)
		if err != nil {
			t.Fatal(err)
		}
		if got.Key != "x:new" || got.Version != 2 || got.Parent != 1 || !bytes.Equal(pdf, pdfBytes("two")) {
			t.Fatalf("got key=%q version=%d parent=%d pdf=%q", got.Key, got.Version, got.Parent, pdf)
		}
		older, _, err := s.Get("x:new", 1)
		if err != nil || older.Parent != 0 || older.Key != "x:new" {
			t.Fatalf("v1 = %+v, %v", older, err)
		}
	})
}

func TestRekeyRefusesAPDFThatIsNotARegularFile(t *testing.T) {
	const bound = time.Second
	pdfBody := pdfBytes("one")

	t.Run("directory", func(t *testing.T) {
		s, _ := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBody)
		pdf := versionFile(s.dir("x:old"), 1, "pdf")
		if err := os.Remove(pdf); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(pdf, 0o700); err != nil {
			t.Fatal(err)
		}
		err := rekeyWithin(t, s, "x:old", "x:new", bound)
		if !errors.Is(err, ErrDamaged) {
			t.Fatalf("err = %v, want ErrDamaged", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		st, statErr := os.Lstat(pdf)
		if statErr != nil || !st.IsDir() {
			t.Fatalf("pdf path = %v, %v; want a directory left in place", st, statErr)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		s, _ := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBody)
		pdf := versionFile(s.dir("x:old"), 1, "pdf")
		if err := os.Remove(pdf); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("mkfifo", pdf).Run(); err != nil {
			t.Fatal(err)
		}
		err := rekeyWithin(t, s, "x:old", "x:new", bound)
		if !errors.Is(err, ErrDamaged) {
			t.Fatalf("err = %v, want ErrDamaged", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		st, statErr := os.Lstat(pdf)
		if statErr != nil || st.Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("pdf path = %v, %v; want the FIFO left in place", st, statErr)
		}
	})

	t.Run("symlink to a real pdf", func(t *testing.T) {
		s, _ := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBody)
		pdf := versionFile(s.dir("x:old"), 1, "pdf")
		real := filepath.Join(t.TempDir(), "real.pdf")
		if err := os.WriteFile(real, pdfBody, 0o600); err != nil {
			t.Fatal(err)
		}
		realBefore, err := os.Stat(real)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(pdf); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, pdf); err != nil {
			t.Fatal(err)
		}
		err = rekeyWithin(t, s, "x:old", "x:new", bound)
		if !errors.Is(err, ErrDamaged) {
			t.Fatalf("err = %v, want ErrDamaged", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		st, statErr := os.Lstat(pdf)
		if statErr != nil || st.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("pdf path = mode %v, %v; want the symlink left in place", st.Mode(), statErr)
		}
		realAfter, err := os.Stat(real)
		if err != nil || !os.SameFile(realBefore, realAfter) || !bytes.Equal(mustRead(t, real), pdfBody) {
			t.Fatalf("symlink target changed: %v", err)
		}
	})
}

func TestRekeyKeepsSchema2InstructionForGet(t *testing.T) {
	s, _ := newStore(t)
	m := goodMeta()
	m.Instruction = "rank the compilers section ahead of serving"
	saved, _ := mustSave(t, s, "x:old", m, pdfBytes("one"))
	if saved.Schema != 2 || saved.Instruction != m.Instruction {
		t.Fatalf("saved schema %d instruction %q", saved.Schema, saved.Instruction)
	}
	if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
		t.Fatal(err)
	}
	got, pdf, err := s.Get("x:new", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "x:new" || got.Schema != 2 || got.Instruction != m.Instruction || !bytes.Equal(pdf, pdfBytes("one")) {
		t.Fatalf("Get = key %q schema %d instruction %q pdf %q", got.Key, got.Schema, got.Instruction, pdf)
	}
	if _, err := os.Lstat(s.dir("x:old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old folder: %v", err)
	}
}

func TestRekeyRefusesUnknownJSONField(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
	editRecord(t, s.dir("x:old"), 1, func(raw []byte) []byte {
		out := bytes.Replace(raw, []byte(`{"schema":2,`), []byte(`{"schema":2,"not_a_field":1,`), 1)
		if bytes.Equal(out, raw) {
			t.Fatal("schema prefix was not in the record")
		}
		return out
	})
	before := snapshotTree(t, filepath.Join(state, "resumes"))
	if err := s.Rekey(context.Background(), "x:old", "x:new"); !errors.Is(err, ErrDamaged) {
		t.Fatalf("err = %v, want ErrDamaged", err)
	}
	assertNewKeyAbsent(t, s, "x:old", "x:new")
	if after := snapshotTree(t, filepath.Join(state, "resumes")); after != before {
		t.Fatalf("unknown field was moved:\n%s\n---\n%s", before, after)
	}
}

func TestRekeyDoesNotCreateAPathForAnEmptyFolderOrAMissingKey(t *testing.T) {
	t.Run("empty folder", func(t *testing.T) {
		s, _ := newStore(t)
		if err := os.MkdirAll(s.dir("x:old"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := s.Rekey(context.Background(), "x:old", "x:new")
		if err == nil {
			t.Fatal("empty folder was rekeyed")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		if _, statErr := os.Lstat(filepath.Join(s.dir("x:old"), keyFile)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("key file = %v, want absent", statErr)
		}
		if _, statErr := os.Lstat(versionFile(s.dir("x:old"), 1, "json")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("v1.json = %v, want absent", statErr)
		}
	})

	t.Run("missing key file", func(t *testing.T) {
		s, _ := newStore(t)
		mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
		dir := s.dir("x:old")
		if err := os.Remove(filepath.Join(dir, keyFile)); err != nil {
			t.Fatal(err)
		}
		raw := mustRead(t, versionFile(dir, 1, "json"))
		pdf := mustRead(t, versionFile(dir, 1, "pdf"))
		err := s.Rekey(context.Background(), "x:old", "x:new")
		if !errors.Is(err, ErrDamaged) {
			t.Fatalf("err = %v, want ErrDamaged", err)
		}
		assertNewKeyAbsent(t, s, "x:old", "x:new")
		if _, statErr := os.Lstat(filepath.Join(dir, keyFile)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("key file reappeared: %v", statErr)
		}
		if got := mustRead(t, versionFile(dir, 1, "json")); !bytes.Equal(got, raw) {
			t.Fatal("record changed")
		}
		if got := mustRead(t, versionFile(dir, 1, "pdf")); !bytes.Equal(got, pdf) {
			t.Fatal("PDF changed")
		}
	})
}

// A record that already names the new key is not rewritten. The sibling that
// still names the old key is, which is how a resumed move finishes.
func TestRekeyDoesNotRewriteARecordThatAlreadyNamesTheNewKey(t *testing.T) {
	s, _ := newStore(t)
	m := goodMeta()
	m.Instruction = "keep the compilers section as written"
	mustSave(t, s, "x:old", m, pdfBytes("one"))
	mustSave(t, s, "x:old", goodMeta(), pdfBytes("two"))
	dir := s.dir("x:old")
	v1 := versionFile(dir, 1, "json")
	editRecord(t, dir, 1, func(raw []byte) []byte {
		if bytes.Count(raw, []byte(`"key":"x:old"`)) != 1 {
			t.Fatalf("key field count = %d", bytes.Count(raw, []byte(`"key":"x:old"`)))
		}
		return bytes.Replace(raw, []byte(`"key":"x:old"`), []byte(`"key":"x:new"`), 1)
	})
	aged := time.Now().Add(-72 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(v1, aged, aged); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(v1)
	if err != nil {
		t.Fatal(err)
	}
	raw := mustRead(t, v1)
	v2Before := mustRead(t, versionFile(dir, 2, "json"))

	if err := s.Rekey(context.Background(), "x:old", "x:new"); err != nil {
		t.Fatal(err)
	}
	moved := versionFile(s.dir("x:new"), 1, "json")
	after, err := os.Stat(moved)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("record that already named the new key was replaced")
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("mtime = %v, want %v", after.ModTime(), before.ModTime())
	}
	if got := mustRead(t, moved); !bytes.Equal(got, raw) {
		t.Fatal("record bytes changed")
	}
	v2 := mustRead(t, versionFile(s.dir("x:new"), 2, "json"))
	if bytes.Equal(v2, v2Before) || !bytes.Contains(v2, []byte(`"key":"x:new"`)) {
		t.Fatal("sibling that named the old key was not rewritten")
	}
	key, err := os.ReadFile(filepath.Join(s.dir("x:new"), keyFile))
	if err != nil || string(key) != "x:new" {
		t.Fatalf("key file = %q, %v", key, err)
	}
	got, pdf, err := s.Get("x:new", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "x:new" || got.Instruction != m.Instruction || !bytes.Equal(pdf, pdfBytes("one")) {
		t.Fatalf("Get = key %q instruction %q", got.Key, got.Instruction)
	}
}

// Rekey waits on the job lock through the caller's context. A cancelled
// context is the wait the lock API already implements; it is not a second timeout.
func TestRekeyCancelledWhileJobLockedDoesNotMove(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
	if _, release, err := s.lockJob(context.Background(), "x:old", false); err != nil {
		t.Fatal(err)
	} else {
		defer release()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- s.Rekey(ctx, "x:old", "x:new")
	}()
	var err error
	select {
	case err = <-done:
		if time.Since(start) >= time.Second {
			t.Fatalf("cancelled Rekey took %s", time.Since(start))
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Rekey waited instead of returning")
	}
	if !errors.Is(err, ErrBusy) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrBusy wrapping context.Canceled", err)
	}
	assertNewKeyAbsent(t, s, "x:old", "x:new")
	if _, pdf, err := s.Get("x:old", 1); err != nil || !bytes.Equal(pdf, pdfBytes("one")) {
		t.Fatalf("old record = %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
