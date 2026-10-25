package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// jrTwoVersions saves v1 (the tailored build: b2, b1) and v2 (an edit: b1), the
// second current, and returns the store.
func jrTwoVersions(t *testing.T, f *fakeResumeRKB) (string, *resumestore.Store) {
	t.Helper()
	_, prov := jrTailor()
	state, st := jrSetup(t, f, prov)
	jrBuild(t)
	out, _ := jrRun(t, 0, jrEditOf("alpha-job", "b1"), "render", "--json", "--fit", jrKey)
	if v := jrSaved(t, out); v.Version != 2 || !v.CurrentMoved {
		t.Fatalf("v2 = %+v", v.Meta)
	}
	return state, st
}

func jrShow(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out, _ := jrRun(t, 0, "", append([]string{"show", "--json"}, args...)...)
	return jrObject(t, out)
}

func jrVersionsOf(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, _ := m["versions"].([]any)
	var out []map[string]any
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestJobsResumeShowListsVersionsWithFreshEligibilityAndThePool(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)

	m := jrShow(t, jrKey)
	if m["eligibility_checked"] != true || m["current"] != float64(2) || m["version"] != float64(2) || m["key"] != jrKey {
		t.Fatalf("show = %v", m)
	}
	vs := jrVersionsOf(t, m)
	if len(vs) != 2 {
		t.Fatalf("%d versions", len(vs))
	}
	for i, v := range vs {
		el, _ := v["eligibility"].(map[string]any)
		if el["state"] != "current" {
			t.Errorf("v%d eligibility = %v", i+1, el)
		}
		// Each carries its checks and fit lines as stored; the choice is only on the asked-for one.
		if _, ok := v["checks"].(map[string]any); !ok {
			t.Errorf("v%d has no checks", i+1)
		}
	}
	if _, has := vs[0]["selection"]; has {
		t.Error("v1's choice is listed though v2 was asked for")
	}
	if sel, _ := vs[1]["selection"].(map[string]any); sel == nil || vs[1]["current"] != true {
		t.Errorf("v2 = %v", vs[1])
	}
	if _, has := vs[1]["used"]; has {
		t.Errorf("v2 is marked used: %v", vs[1])
	}
	pool, _ := m["pool"].(map[string]any)
	if pool == nil || len(pool["entries"].([]any)) != 1 || pool["base"] == nil || pool["fingerprints"] == nil {
		t.Fatalf("pool = %v", m["pool"])
	}
	if _, bad := m["keywords_error"]; bad {
		t.Errorf("keywords_error = %v", m["keywords_error"])
	}
	for _, k := range []string{"check_error", "versions_error"} {
		if _, bad := m[k]; bad {
			t.Errorf("%s = %v", k, m[k])
		}
	}

	// A named version carries its own choice, and only that one.
	m = jrShow(t, jrKey, "v1")
	vs = jrVersionsOf(t, m)
	if m["version"] != float64(1) || vs[0]["selection"] == nil || vs[1]["selection"] != nil {
		t.Errorf("show v1: %v", m)
	}
}

func TestJobsResumeShowNamesWhatWentStale(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	// b1 is reworded and re-reviewed; b2 is removed from the bank altogether.
	jrBankDoc(t, []string{"b1"}, map[string]map[string]any{
		"b1": reviewedBullet("Cut latency by {{clmLat}} in the hot path.", "Cut latency by 42% in the hot path."),
	}, "b2")
	vs := jrVersionsOf(t, jrShow(t, jrKey))
	el1, _ := vs[0]["eligibility"].(map[string]any)
	el2, _ := vs[1]["eligibility"].(map[string]any)
	if el1["state"] != "stale" || !sameStrings(el1["reworded"], "b1") || !sameStrings(el1["gone"], "b2") || el1["ineligible"] != nil {
		t.Errorf("v1 eligibility = %v", el1)
	}
	if el2["state"] != "stale" || !sameStrings(el2["reworded"], "b1") || el2["gone"] != nil {
		t.Errorf("v2 eligibility = %v", el2)
	}
	if ch, _ := el1["inputs_changed"].([]any); len(ch) == 0 {
		t.Errorf("the bank changed, inputs_changed = %v", el1["inputs_changed"])
	}
}

func sameStrings(v any, want ...string) bool {
	l, _ := v.([]any)
	if len(l) != len(want) {
		return false
	}
	for i, w := range want {
		if l[i] != w {
			return false
		}
	}
	return true
}

// --no-check reads stored files only: it runs no rkb, so a broken rkb does not
// matter, and it says plainly that no eligibility was checked.
func TestJobsResumeShowNoCheckReadsStoredFilesOnly(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	f.mu.Lock()
	f.args, f.err = nil, errors.New("rkb must not run")
	f.mu.Unlock()
	m := jrShow(t, "--no-check", jrKey)
	if m["eligibility_checked"] != false || m["pool"] != nil || m["current"] != float64(2) {
		t.Fatalf("show = %v", m)
	}
	for _, v := range jrVersionsOf(t, m) {
		if _, has := v["eligibility"]; has {
			t.Errorf("an unchecked listing carries a verdict: %v", v["eligibility"])
		}
	}
	if len(f.args) != 0 {
		t.Errorf("rkb ran %d times for --no-check", len(f.args))
	}
	if _, has := m["check_error"]; has {
		t.Error("--no-check reports a failed check it never ran")
	}
}

// Every version with a PDF names it, so the app can open any version, not
// only the one just built or used.
func TestJobsResumeShowNamesEachVersionsPDF(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	m := jrShow(t, "--no-check", jrKey)
	vs := jrVersionsOf(t, m)
	if len(vs) != 2 {
		t.Fatalf("versions = %v", vs)
	}
	for _, v := range vs {
		n := int(v["version"].(float64))
		want, err := st.PDFPath(jrKey, n)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := v["pdf_path"].(string); got != want {
			t.Errorf("v%d pdf_path = %q, want %q", n, got, want)
		}
	}
}

// A check that could not run leaves every version "unknown", never "current".
func TestJobsResumeShowWhenTheCheckCannotRunNothingIsCurrent(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	f.mu.Lock()
	f.err = errors.New("rkb is broken")
	f.mu.Unlock()
	m := jrShow(t, jrKey)
	if m["eligibility_checked"] != true || m["check_error"] == nil || m["pool"] != nil {
		t.Fatalf("show = %v", m)
	}
	for _, v := range jrVersionsOf(t, m) {
		el, _ := v["eligibility"].(map[string]any)
		if el["state"] != "unknown" || el["why"] == nil {
			t.Errorf("eligibility = %v", el)
		}
	}
}

func TestJobsResumeShowAJobWithNoVersionsStillGivesThePoolAndNoSelection(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrSetup(t, f, nil)
	m := jrShow(t, jrKey)
	if len(jrVersionsOf(t, m)) != 0 || m["versions"] == nil || m["pool"] == nil || m["version"] != nil || m["current"] != nil {
		t.Errorf("show = %v", m)
	}
	// Asking for a version the job lacks is an error, not an empty answer.
	out, _ := jrRun(t, 1, "", "show", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "not_found" {
		t.Errorf("reply = %+v", r)
	}
}

func TestJobsResumeShowRefusals(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	for _, c := range []struct {
		args []string
		code string
		exit int
	}{
		{[]string{jrKey, "v9"}, "not_found", 1},
		{[]string{jrKey, "two"}, "bad_request", 1},
		{[]string{jrKey, "v10000"}, "bad_request", 1},
		{[]string{"gh:nobody:9"}, "unknown_key", 1},
		{[]string{"gh:a\x07"}, "bad_request", 1},
		{[]string{}, "", 2},
		{[]string{jrKey, "v1", "v2"}, "", 2},
	} {
		out, _ := jrRun(t, c.exit, "", append([]string{"show", "--json"}, c.args...)...)
		if c.exit == 1 {
			if r := jrFail(t, out); r.Code != c.code {
				t.Errorf("show %v: reply = %+v, want %q", c.args, r, c.code)
			}
		}
	}
}

// A version that does not read is reported beside the ones that do, and is never
// taken for the whole job; asking for the damaged one says so.
func TestJobsResumeShowReportsADamagedVersionBesideTheGoodOnes(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	pdf, err := st.PDFPath(jrKey, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(pdf), "v1.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := jrShow(t, "--no-check", jrKey)
	vs := jrVersionsOf(t, m)
	if len(vs) != 1 || vs[0]["version"] != float64(2) || m["versions_error"] == nil || m["versions_error"] == "" {
		t.Fatalf("show = %v", m)
	}
	out, _ := jrRun(t, 1, "", "show", "--json", "--no-check", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "damaged" {
		t.Errorf("reply = %+v", r)
	}
}

func TestJobsResumeShowSpendsNothingAndTakesNoLock(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	p, prov := jrTailor()
	state, _ := jrSetup(t, f, prov)
	jrBuild(t)
	asked := len(p.reqs)
	jrShow(t, jrKey)
	if len(p.reqs) != asked {
		t.Error("show asked the model")
	}
	// A running build holds the build lock; show only reads, so it is not refused.
	jrHoldBuildLock(t, state)
	jrShow(t, jrKey)
	jrShow(t, "--no-check", jrKey)
}

// ---- use -------------------------------------------------------------------

func TestJobsResumeUseBindsAFreshlyCheckedVersion(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	state, st := jrTwoVersions(t, f)
	out, _ := jrRun(t, 0, "", "use", "--json", jrKey, "v1")
	m := jrObject(t, out)
	for _, k := range []string{"version", "pdf_path", "name", "pdf_sha256", "key"} {
		if _, ok := m[k]; !ok {
			t.Errorf("reply lacks %q: %s", k, out)
		}
	}
	if m["version"] != float64(1) || m["name"] != "Resume_Acme_Robotics.pdf" {
		t.Errorf("reply = %s", out)
	}
	path, _ := m["pdf_path"].(string)
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, resumestore.Dir(state)) {
		t.Errorf("pdf_path = %q", path)
	}
	b, err := os.ReadFile(path)
	sum := sha256.Sum256(b)
	if err != nil || hex.EncodeToString(sum[:]) != m["pdf_sha256"] {
		t.Errorf("pdf_sha256 does not match the file: %v", err)
	}
	if used, err := st.Used(jrKey); err != nil || used.Version != 1 {
		t.Fatalf("used = %+v, %v", used, err)
	}
	// Show marks it, beside the current one.
	vs := jrVersionsOf(t, jrShow(t, "--no-check", jrKey))
	if vs[0]["used"] != true || vs[1]["used"] != nil || vs[1]["current"] != true {
		t.Errorf("versions = %v", vs)
	}
}

func TestJobsResumeUseRefusesAStaleVersion(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	jrBankDoc(t, []string{"b1", "b2"}, map[string]map[string]any{
		"b2": reviewedBullet("Built a tracing pipeline for the evaluation harness.", "Built a tracing pipeline for the evaluation harness."),
	})
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	r := jrFail(t, out)
	if r.Code != "stale" || r.Eligibility == nil || r.Eligibility.State != "stale" || strings.Join(r.Eligibility.Reworded, ",") != "b2" {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("a stale version was bound: %v", err)
	}
	// v2 never held b2, so it is still fine.
	jrRun(t, 0, "", "use", "--json", jrKey, "v2")
	if used, err := st.Used(jrKey); err != nil || used.Version != 2 {
		t.Errorf("used = %+v, %v", used, err)
	}
}

// A version that could not be checked is never used: the check failing is not a pass.
func TestJobsResumeUseRefusesWhenTheCheckCannotRun(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	f.mu.Lock()
	f.err = errors.New("rkb is broken")
	f.mu.Unlock()
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "unchecked" {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("an unchecked version was bound: %v", err)
	}
}

func TestJobsResumeUseStoppedMidCheckBindsNothing(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	old := signalContext
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	t.Cleanup(func() { signalContext = old })
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "cancelled" {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("bound after a stop: %v", err)
	}
}

// The store refuses a version whose fit did not pass, even when it is current in
// the bank: use says so with its own code.
func TestJobsResumeUseRefusesAVersionTheStoreCannotBind(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	m, _, err := st.Get(jrKey, 2)
	if err != nil {
		t.Fatal(err)
	}
	m.Checks.Fit = "not_run"
	m.Pages = 0
	if _, _, err := st.Save(context.Background(), jrKey, m, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v3")
	if r := jrFail(t, out); r.Code != "not_usable" {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("bound: %v", err)
	}
}

// Every subcommand is bounded by its own deadline; a check that runs out of time
// is a refusal with the timeout code, never a bound version.
func TestJobsResumeDeadlinesBoundTheCommands(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	old := resumeUseDeadline
	resumeUseDeadline = 1 // one nanosecond
	t.Cleanup(func() { resumeUseDeadline = old })
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "timeout" {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("bound after the deadline: %v", err)
	}

	// show --no-check keeps what it read and says what it could not get.
	oldShow := resumeShowStoredDeadline
	resumeShowStoredDeadline = 1
	t.Cleanup(func() { resumeShowStoredDeadline = oldShow })
	m := jrShow(t, "--no-check", jrKey)
	if len(jrVersionsOf(t, m)) != 2 || m["keywords_error"] == nil {
		t.Errorf("show = %v", m)
	}
}

func TestJobsResumeUseRefusals(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	jrTwoVersions(t, f)
	for _, c := range []struct {
		args []string
		code string
		exit int
	}{
		{[]string{jrKey, "v9"}, "not_found", 1},
		{[]string{jrKey, "1"}, "bad_request", 1},
		{[]string{jrKey, "v01"}, "bad_request", 1},
		{[]string{"gh:nobody:9", "v1"}, "unknown_key", 1},
		{[]string{jrKey}, "", 2},
		{[]string{}, "", 2},
	} {
		out, _ := jrRun(t, c.exit, "", append([]string{"use", "--json"}, c.args...)...)
		if c.exit == 1 {
			if r := jrFail(t, out); r.Code != c.code {
				t.Errorf("use %v: reply = %+v, want %q", c.args, r, c.code)
			}
		}
	}
}

// A damaged PDF is never bound.
func TestJobsResumeUseRefusesADamagedPDF(t *testing.T) {
	f := &fakeResumeRKB{pages: 2, pdf: testPDF}
	_, st := jrTwoVersions(t, f)
	pdf, _ := st.PDFPath(jrKey, 1)
	if err := os.WriteFile(pdf, []byte("%PDF-1.7 tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := jrRun(t, 1, "", "use", "--json", jrKey, "v1")
	if r := jrFail(t, out); r.Code != "damaged" {
		t.Fatalf("reply = %+v", r)
	}
	if _, err := st.Used(jrKey); !errors.Is(err, resumestore.ErrNotFound) {
		t.Errorf("bound: %v", err)
	}
}

func TestJobsResumeIsInTheUsage(t *testing.T) {
	setup(t)
	_, stderr := mustCode(t, 2, "bogus")
	if !strings.Contains(stderr, "jobs resume build|render|show|use|revalidate|diff|current|coverage|gaps") {
		t.Errorf("usage does not list jobs resume:\n%s", stderr)
	}
	_, stderr = mustCode(t, 2, "jobs", "resume")
	if !strings.Contains(stderr, "expected build, render, show, use, revalidate, diff, current, coverage or gaps") {
		t.Errorf("stderr = %s", stderr)
	}
	_, stderr = mustCode(t, 2, "jobs", "resume", "sideways")
	if !strings.Contains(stderr, `unknown subcommand "sideways"`) {
		t.Errorf("stderr = %s", stderr)
	}
}
