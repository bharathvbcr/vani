package jobview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resumestore"
)

func resumeMeta(mod func(*resumestore.Meta)) resumestore.Meta {
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
	return m
}

func resumeStore(t *testing.T) (*resumestore.Store, string) {
	t.Helper()
	state := t.TempDir()
	st, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return st, state
}

func saveResume(t *testing.T, st *resumestore.Store, key string, m resumestore.Meta) {
	t.Helper()
	if _, _, err := st.Save(context.Background(), key, m, []byte("%PDF-1.7\nx\n%%EOF\n")); err != nil {
		t.Fatal(err)
	}
}

// resumeFor is ResumeFor over a fresh walk of st, as a listing does it.
func resumeFor(t *testing.T, st *resumestore.Store, key string) *ResumeSummary {
	t.Helper()
	x, _ := st.Summaries() // a walk that failed answers every job with its error
	return ResumeFor(x, key)
}

func TestResumeSummaryNone(t *testing.T) {
	st, _ := resumeStore(t)
	if got := resumeFor(t, st, "gh:acme:1"); got != nil {
		t.Fatalf("a job with no versions has a summary: %+v", got)
	}
	if got := ResumeFor(nil, "gh:acme:1"); got != nil {
		t.Fatalf("no store gave %+v", got)
	}
	j := Job{Key: "gh:acme:1"}
	raw, err := json.Marshal(j)
	if err != nil || strings.Contains(string(raw), `"resume"`) {
		t.Fatalf("a job without a resume writes the field: %s %v", raw, err)
	}
}

func TestResumeSummaryCurrentOnly(t *testing.T) {
	st, _ := resumeStore(t)
	saveResume(t, st, "gh:acme:1", resumeMeta(func(m *resumestore.Meta) { m.Pages = 2; m.Checks.Fit = "soft"; m.Source = "edited" }))
	got := resumeFor(t, st, "gh:acme:1")
	want := ResumeSummary{Current: 1, Versions: 1, Pages: 2, Fit: "soft", Eligibility: "current", Source: "edited"}
	if got == nil || *got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
	raw, _ := json.Marshal(Job{Key: "gh:acme:1", Resume: got})
	for _, f := range []string{`"resume":{`, `"current":1`, `"versions":1`, `"fit":"soft"`} {
		if !strings.Contains(string(raw), f) {
			t.Errorf("%s missing from %s", f, raw)
		}
	}
	if strings.Contains(string(raw), `"used"`) || strings.Contains(string(raw), `"error"`) || strings.Contains(string(raw), "selection") {
		t.Errorf("unexpected fields in %s", raw)
	}
}

func TestResumeSummaryCurrentAndUsed(t *testing.T) {
	st, _ := resumeStore(t)
	saveResume(t, st, "gh:acme:1", resumeMeta(nil))
	saveResume(t, st, "gh:acme:1", resumeMeta(func(m *resumestore.Meta) { m.Parent = 1; m.Pages = 2; m.Source = "edited" }))
	if _, err := st.Use(context.Background(), "gh:acme:1", 1); err != nil {
		t.Fatal(err)
	}
	got := resumeFor(t, st, "gh:acme:1")
	want := ResumeSummary{Current: 2, Used: 1, Versions: 2, Pages: 2, Fit: "passed", Eligibility: "current", Source: "edited"}
	if got == nil || *got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}

func TestResumeSummaryParentlessVersionKeepsCurrent(t *testing.T) {
	st, _ := resumeStore(t)
	saveResume(t, st, "gh:acme:1", resumeMeta(nil))
	saveResume(t, st, "gh:acme:1", resumeMeta(nil)) // parentless: current stays v1
	got := resumeFor(t, st, "gh:acme:1")
	if got == nil || got.Versions != 2 || got.Current != 1 || got.Used != 0 {
		t.Fatalf("summary = %+v", got)
	}
}

func TestResumeSummaryDamagedIsAnErrorNotAbsence(t *testing.T) {
	st, state := resumeStore(t)
	saveResume(t, st, "gh:acme:1", resumeMeta(nil))
	saveResume(t, st, "gh:acme:2", resumeMeta(nil))
	// Damage job 1: its PDF is gone while its record names a hash.
	var damaged string
	root := resumestore.Dir(state)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		key, _ := os.ReadFile(filepath.Join(root, e.Name(), "key"))
		if string(key) == "gh:acme:1" {
			damaged = filepath.Join(root, e.Name())
		}
	}
	if damaged == "" {
		t.Fatal("job folder not found")
	}
	if err := os.Remove(filepath.Join(damaged, "v1.pdf")); err != nil {
		t.Fatal(err)
	}
	got := resumeFor(t, st, "gh:acme:1")
	if got == nil || got.Error != "unreadable" || got.Versions != 0 || got.Current != 0 {
		t.Fatalf("damaged job = %+v, want an unreadable summary", got)
	}
	raw, _ := json.Marshal(Job{Key: "gh:acme:1", Resume: got})
	if !strings.Contains(string(raw), `"error":"unreadable"`) {
		t.Fatalf("error not on the wire: %s", raw)
	}
	// The other job is unaffected.
	if other := resumeFor(t, st, "gh:acme:2"); other == nil || other.Error != "" || other.Current != 1 {
		t.Fatalf("job 2 = %+v", other)
	}
}

func TestResumeSummaryOtherSchemaIsUnsupported(t *testing.T) {
	st, state := resumeStore(t)
	saveResume(t, st, "gh:acme:1", resumeMeta(nil))
	root := resumestore.Dir(state)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		p := filepath.Join(root, e.Name(), "v1.json")
		if b, err := os.ReadFile(p); err == nil {
			if err := os.WriteFile(p, []byte(strings.Replace(string(b), `"schema":2`, `"schema":3`, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := resumeFor(t, st, "gh:acme:1")
	if got == nil || got.Error != "unsupported" {
		t.Fatalf("summary = %+v", got)
	}
}

// A schema 1 record (written before the instruction existed) summarises exactly
// as a schema 2 one does: the job list does not read the instruction.
func TestResumeSummaryOfASchema1RecordIsTheSame(t *testing.T) {
	st, state := resumeStore(t)
	saveResume(t, st, "gh:acme:1", resumeMeta(nil))
	want := resumeFor(t, st, "gh:acme:1")
	root := resumestore.Dir(state)
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		p := filepath.Join(root, e.Name(), "v1.json")
		if b, err := os.ReadFile(p); err == nil {
			if err := os.WriteFile(p, []byte(strings.Replace(string(b), `"schema":2`, `"schema":1`, 1)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	got := resumeFor(t, st, "gh:acme:1")
	if got == nil || want == nil || *got != *want || got.Error != "" {
		t.Fatalf("schema 1 summary = %+v, schema 2 = %+v", got, want)
	}
}

func TestResumeSummaryUnusableStoreRoot(t *testing.T) {
	state := t.TempDir()
	// resumes is a file: every read is an error, none is "no resume".
	if err := os.WriteFile(filepath.Join(state, "resumes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := resumestore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := resumeFor(t, st, "gh:acme:1"); got == nil || got.Error != "unreadable" {
		t.Fatalf("summary = %+v", got)
	}
}
