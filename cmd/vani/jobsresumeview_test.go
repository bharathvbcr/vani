package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

func rvSave(t *testing.T, state, key string, mod func(*resumestore.Meta)) {
	t.Helper()
	st, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	m := resumestore.Meta{
		Source:    "tailored",
		Checks:    resumestore.Checks{Eligibility: "current", Lint: "passed", Fit: "passed", Figures: "verified"},
		Pages:     1,
		Selection: json.RawMessage(`{"sections":[]}`),
		Bullets:   []resumestore.BulletRef{},
	}
	if mod != nil {
		mod(&m)
	}
	if _, _, err := st.Save(context.Background(), key, m, []byte("%PDF-1.7\nx\n%%EOF\n")); err != nil {
		t.Fatal(err)
	}
}

func rvSetup(t *testing.T) string {
	t.Helper()
	_, state := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	rtSeed(t, state,
		rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"},
		rtJob{key: "gh:acme:2", title: "Platform Engineer", fit: "adjacent", p: "0.9"})
	return state
}

func TestJobsShowAndListCarryTheResumeSummary(t *testing.T) {
	state := rvSetup(t)
	if j := rtShow(t, "gh:acme:1"); j.Resume != nil {
		t.Fatalf("a job with no resume shows %+v", j.Resume)
	}
	rvSave(t, state, "gh:acme:1", nil)
	rvSave(t, state, "gh:acme:1", func(m *resumestore.Meta) { m.Parent, m.Pages, m.Source = 1, 2, "edited" })
	if _, err := mustUse(t, state, "gh:acme:1", 1); err != nil {
		t.Fatal(err)
	}
	j := rtShow(t, "gh:acme:1")
	if j.Resume == nil || j.Resume.Current != 2 || j.Resume.Used != 1 || j.Resume.Versions != 2 || j.Resume.Pages != 2 || j.Resume.Source != "edited" || j.Resume.Fit != "passed" || j.Resume.Eligibility != "current" || j.Resume.Error != "" {
		t.Fatalf("show.resume = %+v", j.Resume)
	}
	var seen int
	for _, lj := range listJSON(t).Jobs {
		switch lj.Key {
		case "gh:acme:1":
			seen++
			if lj.Resume == nil || lj.Resume.Current != 2 {
				t.Fatalf("list row resume = %+v", lj.Resume)
			}
		case "gh:acme:2":
			seen++
			if lj.Resume != nil {
				t.Fatalf("a job with no resume lists %+v", lj.Resume)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d of the two jobs", seen)
	}
}

func TestDamagedResumeStoreDoesNotBreakJobsAndIsNotAbsence(t *testing.T) {
	state := rvSetup(t)
	rvSave(t, state, "gh:acme:1", nil)
	root := resumestore.Dir(state)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if e.IsDir() {
			if err := os.Remove(filepath.Join(root, e.Name(), "v1.pdf")); err != nil {
				t.Fatal(err)
			}
		}
	}
	j := rtShow(t, "gh:acme:1")
	if j.Resume == nil || j.Resume.Error != "unreadable" {
		t.Fatalf("show.resume = %+v, want error unreadable", j.Resume)
	}
	if j.Title == "" {
		t.Fatal("the job did not load")
	}
	if l := listJSON(t); len(l.Jobs) != 2 {
		t.Fatalf("list returned %d jobs", len(l.Jobs))
	}
}

func mustUse(t *testing.T, state, key string, n int) (resumestore.Meta, error) {
	t.Helper()
	st, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return st.Use(context.Background(), key, n)
}

func TestMarkAppliedRecordsTheUsedResumeVersion(t *testing.T) {
	state := rvSetup(t)
	rvSave(t, state, "gh:acme:1", nil)
	rvSave(t, state, "gh:acme:1", func(m *resumestore.Meta) { m.Parent = 1 })
	if _, err := mustUse(t, state, "gh:acme:1", 2); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "jobs", "mark", "--json", "gh:acme:1", "applied")
	m, err := marks.Read(marks.Dir(state))
	if err != nil || m["gh:acme:1"].Mark != marks.Applied || m["gh:acme:1"].ResumeVersion != 2 {
		t.Fatalf("marks %+v %v", m["gh:acme:1"], err)
	}
	// Saving does not touch the version; hiding drops it with the mark.
	mustCode(t, 0, "jobs", "mark", "--json", "gh:acme:1", "hidden")
	if m, _ := marks.Read(marks.Dir(state)); m["gh:acme:1"].ResumeVersion != 0 {
		t.Fatalf("hidden kept a sent version: %+v", m["gh:acme:1"])
	}
}

func TestMarkAppliedWithoutAUsedVersionRecordsNone(t *testing.T) {
	state := rvSetup(t)
	rvSave(t, state, "gh:acme:1", nil) // saved and current, never used
	mustCode(t, 0, "jobs", "mark", "--json", "gh:acme:1", "applied")
	mustCode(t, 0, "jobs", "mark", "--json", "gh:acme:2", "applied") // no resume at all
	m, _ := marks.Read(marks.Dir(state))
	for _, k := range []string{"gh:acme:1", "gh:acme:2"} {
		if e := m[posting.Key(k)]; e.Mark != marks.Applied || e.ResumeVersion != 0 {
			t.Fatalf("%s: %+v", k, e)
		}
	}
}

// A resume store that cannot be read must not stop the owner from recording
// that they applied; the mark is made without a version and stderr says so.
func TestMarkAppliedWithDamagedResumeStoreStillMarks(t *testing.T) {
	state := rvSetup(t)
	rvSave(t, state, "gh:acme:1", nil)
	if _, err := mustUse(t, state, "gh:acme:1", 1); err != nil {
		t.Fatal(err)
	}
	root := resumestore.Dir(state)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if e.IsDir() {
			if err := os.WriteFile(filepath.Join(root, e.Name(), "used"), []byte("garbage"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, stderr := mustCode(t, 0, "jobs", "mark", "--json", "gh:acme:1", "applied")
	if !strings.Contains(stderr, "used resume version could not be read") {
		t.Fatalf("stderr = %q", stderr)
	}
	m, _ := marks.Read(marks.Dir(state))
	if e := m[posting.Key("gh:acme:1")]; e.Mark != marks.Applied || e.ResumeVersion != 0 {
		t.Fatalf("%+v", e)
	}
}
