package marks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppliedRecordsTheSentResumeVersion(t *testing.T) {
	dir := Dir(t.TempDir())
	e, err := SetApplied(dir, k1, 3, t0)
	if err != nil {
		t.Fatal(err)
	}
	if e.Mark != Applied || e.ResumeVersion != 3 || !e.MarkedAt.Equal(t0) {
		t.Fatalf("entry %+v", e)
	}
	m, err := Read(dir)
	if err != nil || m[k1].ResumeVersion != 3 || m[k1].Mark != Applied {
		t.Fatalf("read back %+v %v", m[k1], err)
	}
	// A note edit leaves the mark, and the version that belongs to it, alone.
	if _, err := SetNote(dir, k1, "sent the v3", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if m, _ := Read(dir); m[k1].ResumeVersion != 3 || m[k1].Note == "" {
		t.Fatalf("a note edit changed the version: %+v", m[k1])
	}
	// Any later mark replaces the version with its own: hiding drops it,
	// applying again without one records none.
	if e, err := SetMark(dir, k1, Hidden, t0.Add(2*time.Minute)); err != nil || e.ResumeVersion != 0 {
		t.Fatalf("hidden kept the version: %+v %v", e, err)
	}
	if e, err := SetApplied(dir, k1, 5, t0.Add(3*time.Minute)); err != nil || e.ResumeVersion != 5 {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err := SetApplied(dir, k1, 0, t0.Add(4*time.Minute)); err != nil || e.Mark != Applied || e.ResumeVersion != 0 {
		t.Fatalf("applied with no version kept the old one: %+v %v", e, err)
	}
	if e, err := SetMark(dir, k1, None, t0.Add(5*time.Minute)); err != nil || e.ResumeVersion != 0 {
		t.Fatalf("clear kept the version: %+v %v", e, err)
	}
}

func TestResumeVersionRules(t *testing.T) {
	dir := Dir(t.TempDir())
	for _, v := range []int{-1, MaxResumeVersion + 1} {
		if _, err := SetApplied(dir, k1, v, t0); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetApplied(%d) = %v", v, err)
		}
	}
	// Only an applied mark carries one: a hand-written line for a saved mark is refused.
	line := `{"v":1,"at":"2026-10-19T09:00:00Z","key":"ab:cohere:87e9fd4e","mark":"saved","resume_version":2}` + "\n"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	good := `{"v":1,"at":"2026-10-19T08:00:00Z","key":"ab:cohere:87e9fd4e","mark":"applied"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, lockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(good+line+good), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a resume version on a saved mark read as %v", err)
	}
	if _, err := SetMark(dir, k1, Saved, t0); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("write onto it = %v", err)
	}
}

// A marks file written before the field existed must read exactly as before.
func TestOldMarksFilesReadWithoutResumeVersion(t *testing.T) {
	dir := Dir(t.TempDir())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"v":1,"at":"2026-10-19T09:00:00Z","key":"ab:cohere:87e9fd4e","mark":"applied"}` + "\n" +
		`{"v":1,"at":"2026-10-19T09:05:00Z","key":"ab:cohere:87e9fd4e","note":"call back"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, lockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := m[k1]
	if e.Mark != Applied || e.ResumeVersion != 0 || e.Note != "call back" {
		t.Fatalf("entry %+v", e)
	}
	// And a new line lands on an old file; the entry encodes without the field when it is 0.
	if _, err := SetApplied(dir, "ab:other:1", 2, t0); err != nil {
		t.Fatal(err)
	}
	if m, _ := Read(dir); m[k1].ResumeVersion != 0 || m["ab:other:1"].ResumeVersion != 2 {
		t.Fatalf("%+v", m)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, fileName))
	if strings.Count(string(raw), "resume_version") != 1 {
		t.Fatalf("the field was written for an entry without one:\n%s", raw)
	}
}

func TestCompactionKeepsTheResumeVersion(t *testing.T) {
	dir := Dir(t.TempDir())
	if _, err := SetApplied(dir, k1, 4, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := SetNote(dir, k1, "n", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := Compact(dir); err != nil {
		t.Fatal(err)
	}
	m, err := Read(dir)
	if err != nil || m[k1].ResumeVersion != 4 || m[k1].Mark != Applied || !m[k1].MarkedAt.Equal(t0) {
		t.Fatalf("after compaction %+v %v", m[k1], err)
	}
}
