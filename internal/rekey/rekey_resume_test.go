//go:build !windows

package rekey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

func hex64(c byte) string { return string(bytes.Repeat([]byte{c}, 64)) }

// saveResume files one version under key, with a PDF when withPDF.
func saveResume(t *testing.T, s *resumestore.Store, key posting.Key, withPDF bool) resumestore.Meta {
	t.Helper()
	m := resumestore.Meta{
		Source:    "tailored",
		Checks:    resumestore.Checks{Eligibility: "current", Lint: "passed", Fit: "passed", Figures: "verified"},
		Pages:     1,
		Selection: json.RawMessage(`{"sections":[]}`),
		Bullets:   []resumestore.BulletRef{{ID: "b1", TextHash: "0123456789ab"}},
		Prints:    resumestore.Fingerprints{Bank: hex64('a'), BaseSelection: hex64('b'), Selections: hex64('c'), Claims: hex64('d'), RKB: hex64('e')},
	}
	var pdf []byte
	if withPDF {
		pdf = []byte("%PDF-1.7\nresume of " + string(key) + "\n%%EOF\n")
	} else {
		m.Checks.Fit, m.Pages = "not_run", 0
	}
	saved, _, err := s.Save(context.Background(), string(key), m, pdf)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

// seedResumes gives the C++ posting two versions (current v1, used v2) and the
// C# posting one; the plain posting, which does not move, gets one too.
func seedResumes(t *testing.T, state string) *resumestore.Store {
	t.Helper()
	rs, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	cppKey := oldKey(t, cpp)
	saveResume(t, rs, cppKey, true)
	saveResume(t, rs, cppKey, true)
	if err := rs.SetCurrent(context.Background(), string(cppKey), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Use(context.Background(), string(cppKey), 2); err != nil {
		t.Fatal(err)
	}
	csharp := cpp
	csharp.Company, csharp.Title = "Globex", "Senior C# Engineer"
	saveResume(t, rs, oldKey(t, csharp), true)
	saveResume(t, rs, oldKey(t, plain), true)
	return rs
}

func checkResumesMoved(t *testing.T, state string) {
	t.Helper()
	rs, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	csharp := cpp
	csharp.Company, csharp.Title = "Globex", "Senior C# Engineer"
	for _, p := range []posting.Posting{cpp, csharp} {
		ok, nk := string(oldKey(t, p)), string(newKey(t, p))
		if _, err := rs.List(ok); !errors.Is(err, resumestore.ErrNotFound) {
			t.Errorf("%s: the old key still has resumes: %v", p.Title, err)
		}
		if ms, err := rs.List(nk); err != nil || len(ms) == 0 {
			t.Errorf("%s: List(new) = %d, %v", p.Title, len(ms), err)
		}
	}
	nk := string(newKey(t, cpp))
	if cur, err := rs.Current(nk); err != nil || cur.Version != 1 || cur.Key != nk {
		t.Errorf("current = %+v, %v", cur, err)
	}
	used, err := rs.Used(nk)
	if err != nil || used.Version != 2 {
		t.Fatalf("used = %+v, %v", used, err)
	}
	if _, pdf, err := rs.Get(nk, 2); err != nil || !bytes.Contains(pdf, []byte("resume of "+string(oldKey(t, cpp)))) {
		t.Errorf("the used PDF did not move byte for byte: %v", err)
	}
	// The posting that did not move keeps its folder.
	if ms, err := rs.List(string(oldKey(t, plain))); err != nil || len(ms) != 1 {
		t.Errorf("the unmoved posting's resumes: %d %v", len(ms), err)
	}
}

// The resume folder moves with the posting, beside its applied mark, which
// names a version the new key now has.
func TestResumeVersionsMoveWithTheirPosting(t *testing.T) {
	state := t.TempDir()
	seed(t, state)
	seedResumes(t, state)
	if _, err := marks.SetApplied(marks.Dir(state), oldKey(t, cpp), 2, t0); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(state, false, run)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ResumesMoved != 2 || rep.MarksMoved != 2 {
		t.Fatalf("report %+v", rep)
	}
	checkResumesMoved(t, state)
	m, err := marks.Read(marks.Dir(state))
	if err != nil {
		t.Fatal(err)
	}
	nk := newKey(t, cpp)
	rs, _ := resumestore.Open(state)
	used, err := rs.Used(string(nk))
	if err != nil || m[nk].Mark != marks.Applied || m[nk].ResumeVersion != used.Version {
		t.Fatalf("the applied mark's version %d is not the version the new key holds (%+v, %v)", m[nk].ResumeVersion, used, err)
	}
	// The migration is done and a second run is refused, as before.
	if _, err := Run(state, false, run); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("second run: %v", err)
	}
}

// A resume folder already under the new key is a collision, refused before the
// ledger, the marks or the resumes are touched, in a dry run too.
func TestCollisionWithAResumeFolderIsRefusedBeforeAnythingChanges(t *testing.T) {
	for _, dry := range []bool{false, true} {
		state := t.TempDir()
		seed(t, state)
		rs := seedResumes(t, state)
		// An attempts-only folder counts: Rekey would refuse it too.
		if err := rs.AppendAttempt(context.Background(), string(newKey(t, cpp)), resumestore.Attempt{Code: "timeout"}); err != nil {
			t.Fatal(err)
		}
		before := events(t, state)
		_, err := Run(state, dry, run)
		if !errors.Is(err, ErrCollision) {
			t.Fatalf("dry=%v: err = %v, want ErrCollision", dry, err)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("resume folder")) {
			t.Fatalf("the refusal does not name the resume folder: %v", err)
		}
		if !bytes.Equal(events(t, state), before) {
			t.Fatal("the ledger changed on a refused run")
		}
		if _, err := os.Stat(filepath.Join(state, "ledger", MarkerName)); !os.IsNotExist(err) {
			t.Fatalf("a marker was written: %v", err)
		}
		if ms, err := rs.List(string(oldKey(t, cpp))); err != nil || len(ms) != 2 {
			t.Fatalf("the old job was touched: %d %v", len(ms), err)
		}
	}
}

// A dry run reports the resume moves it would make and moves nothing.
func TestDryRunLeavesResumesAlone(t *testing.T) {
	state := t.TempDir()
	seed(t, state)
	rs := seedResumes(t, state)
	rep, err := Run(state, true, run)
	if err != nil || rep.ResumesMoved != 0 || len(rep.Changes) != 2 {
		t.Fatalf("report %+v, %v", rep, err)
	}
	if ms, err := rs.List(string(oldKey(t, cpp))); err != nil || len(ms) != 2 {
		t.Fatalf("a dry run moved a resume folder: %d %v", len(ms), err)
	}
}

// A crash after the first resume folder moved (and one before any) is finished
// by the next run, which ends where an uninterrupted run does.
func TestCrashBetweenResumeMovesIsFinishedByTheNextRun(t *testing.T) {
	for _, crashAfter := range []int{0, 1} {
		state := t.TempDir()
		seed(t, state)
		seedResumes(t, state)
		boom := errors.New("crash in the resume step")
		calls := 0
		crash = func(s string) error {
			if s == "marks" && crashAfter == 0 {
				return boom // part way through the marks, before any folder moves
			}
			if s == "resumes" {
				calls++
				if calls == crashAfter {
					return boom
				}
			}
			return nil
		}
		_, err := Run(state, false, run)
		crash = func(string) error { return nil }
		if !errors.Is(err, boom) {
			t.Fatalf("crashAfter=%d: first run: %v", crashAfter, err)
		}
		rep, err := Run(state, false, run)
		if err != nil || !rep.Resumed {
			t.Fatalf("crashAfter=%d: rerun: %+v %v", crashAfter, rep, err)
		}
		checkResumesMoved(t, state)
		if _, err := Run(state, false, run); !errors.Is(err, ErrAlreadyRun) {
			t.Fatalf("a third run: %v", err)
		}
	}
}
