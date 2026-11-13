package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/clusterstudy"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// studyState seeds a state dir with n kept postings (half read as fits by the
// teacher, categories alternating independently of that), nDropped dropped
// ones (whose text the ledger fold cuts out), one posting the semantic log has
// no vectors for, and warms the chunk-vector log for the rest.
func studyState(t *testing.T, n, nDropped int) (state string, keys []string) {
	t.Helper()
	_, state = setup(t)
	mustCode(t, 0, "init")
	l, err := ledger.Open(filepath.Join(state, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	var docs []semantic.Doc
	add := func(key string, st posting.Status, ann map[string]string) {
		p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: "Engineer " + key,
			Description: "Requirements:\n- build services for " + key + "\n- operate clusters and write go tools " + key}
		e := ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: st, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		docs = append(docs, semantic.Doc{Key: key, Title: p.Title, Description: p.Description})
		keys = append(keys, key)
	}
	for i := 0; i < n; i++ {
		cat := []string{"ai-engineer", "other"}[i%2]
		add(fmt.Sprintf("gh:k:%03d", i), posting.Digested, map[string]string{ledger.AnnRank: fmt.Sprint(i + 1), "triage.category": cat,
			"triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "0.9000"})
	}
	for i := 0; i < nDropped; i++ {
		add(fmt.Sprintf("gh:d:%03d", i), posting.Dropped, map[string]string{"triage.backend": "title", "triage.role_fit": "mismatch_domain", "triage.category": "other"})
	}
	// A posting the log will have no vectors for.
	p := posting.Posting{Key: "gh:x:000", ATS: "gh", Company: "Acme", Title: "Unwarmed", Description: "Nothing embedded this."}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Digested, Posting: &p}); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	prof := semantic.Profile{Chunks: []semantic.Chunk{{Label: "skills", Text: "Go services and tooling", Weight: 1}, {Label: "role", Text: "Backend engineer", Weight: 1}}, Target: "backend engineer"}
	m, err := semantic.NewMatcher(semantic.NewFakeEmbedder(64, nil), prof, semanticDir(state))
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := m.Warm(context.Background(), docs, 1000); err != nil {
		t.Fatalf("Warm: %v %+v", err, rep)
	}
	return state, keys
}

// seedSonnet stores a Sonnet jd_triage reading per key: strong_fit for the
// first half of the kept postings, mismatch_domain for the rest.
func seedSonnet(t *testing.T, state string, keys []string, fit func(i int) string) {
	t.Helper()
	store, err := eval.OpenTeacherStore(filepath.Join(state, eval.TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		v := fit(i)
		if v == "" {
			continue
		}
		row := eval.TeacherRow{Key: posting.Key(k), TextSHA256: "t", Model: sonnetReader, FixtureSHA: "f", PromptHash: "p",
			Slots: map[string]eval.TeacherSlot{"role_fit": {Reading: "answered", Value: v}}, At: time.Now()}
		if err := store.Append(row); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshotTree is every file under dir with its size and mtime, to show a
// command left the state alone.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fmt.Sprintf("%v %d %d", d.IsDir(), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExportPooledVectorsWritesOneRowPerPostingWithVectorsAndTouchesNoState(t *testing.T) {
	state, keys := studyState(t, 12, 3)
	out := filepath.Join(t.TempDir(), "study")
	before := snapshotTree(t, state)
	o, _ := mustCode(t, 0, "export", "pooled-vectors", "--out", out, "--json")
	var r pooledExportReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	if r.Ledger != len(keys)+1 || r.Exported != len(keys) || r.NoRecord != 1 || r.TextUnavailable != 0 || r.Stale+r.Corrupt+r.Degenerate+r.NoText != 0 {
		t.Errorf("funnel %+v for %d warmed postings (+1 unwarmed)", r, len(keys))
	}
	if r.N != len(keys) || r.Dim != 64 || r.DistinctTexts != len(keys) || r.LogRecords != len(keys) || !strings.HasPrefix(r.Model, "fake-bow") {
		t.Errorf("reply %+v", r)
	}
	got, err := clusterstudy.ReadKeys(filepath.Join(out, pooledKeysFile))
	if err != nil || len(got) != len(keys) {
		t.Fatalf("keys %v, %v", got, err)
	}
	for _, d := range got { // the dropped ones are there: their text was read back
		if strings.HasPrefix(d, "gh:d:") {
			goto dropped
		}
	}
	t.Error("no dropped posting in the export: the evicted bodies were not read back")
dropped:
	if st, err := os.Stat(filepath.Join(out, pooledVectorsFile)); err != nil || st.Size() != int64(52+4*64*len(keys)) {
		t.Errorf("vectors file %v, %v", st, err)
	}
	if after := snapshotTree(t, state); len(after) != len(before) {
		t.Errorf("state dir changed: %d entries before, %d after", len(before), len(after))
	} else {
		for p, v := range before {
			if after[p] != v {
				t.Errorf("%s changed: %q -> %q", p, v, after[p])
			}
		}
	}
}

func TestExportPooledVectorsRefusesWhatItCannotDoSafely(t *testing.T) {
	state, _ := studyState(t, 4, 0)
	for name, args := range map[string][]string{
		"no --out":           {"export", "pooled-vectors"},
		"out in the state":   {"export", "pooled-vectors", "--out", filepath.Join(state, "study")},
		"out is the state":   {"export", "pooled-vectors", "--out", state},
		"max of zero":        {"export", "pooled-vectors", "--out", t.TempDir(), "--max", "0"},
		"max over the bound": {"export", "pooled-vectors", "--out", t.TempDir(), "--max", "999999999"},
		"stray argument":     {"export", "pooled-vectors", "--out", t.TempDir(), "extra"},
	} {
		code, _, _ := do(t, args...)
		if code != exitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, exitUsage)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "study")); err == nil {
		t.Error("an --out inside the state dir was written")
	}
	// A bound lower than the postings that have vectors refuses, not samples.
	out := filepath.Join(t.TempDir(), "o")
	code, _, e := do(t, "export", "pooled-vectors", "--out", out, "--max", "2")
	if code != exitRuntime || !strings.Contains(e, "--max") {
		t.Errorf("--max below the row count: exit %d, %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(out, pooledVectorsFile)); err == nil {
		t.Error("a refused export left a vectors file")
	}
}

func TestExportPooledVectorsWithoutALogIsAnErrorNotAnEmptyFile(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedLedger(t, state, fixture())
	out := filepath.Join(t.TempDir(), "o")
	code, _, e := do(t, "export", "pooled-vectors", "--out", out)
	if code != exitRuntime || !strings.Contains(e, "export pooled-vectors") {
		t.Errorf("exit %d: %s", code, e)
	}
	if _, err := os.Stat(filepath.Join(out, pooledVectorsFile)); err == nil {
		t.Error("an export with no log wrote a vectors file")
	}
	if _, err := os.Stat(filepath.Join(state, "semantic")); err == nil {
		t.Error("the export created the semantic dir")
	}
}

func compareJSON(t *testing.T, args ...string) strataReply {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"strata", "compare", "--json"}, args...)...)
	var r strataReply
	if err := json.Unmarshal([]byte(o), &r); err != nil {
		t.Fatalf("%v\n%s", err, o)
	}
	return r
}

func writeStudySummary(t *testing.T, dir string, keys []string, runs []clusterstudy.FitRun, labels map[string][]uint32) string {
	t.Helper()
	sum := clusterstudy.KeysSHA256(keys)
	for _, r := range runs {
		if err := os.WriteFile(filepath.Join(dir, r.Assign), clusterstudy.EncodeAssignments(sum, r.K, labels[r.Assign]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(clusterstudy.FitSummary{Format: clusterstudy.FitSummaryFormat, Vectors: "pooled.vectors", N: len(keys), Dim: 64,
		KeysSHA256: fmt.Sprintf("%x", sum[:]), Spherical: true, Nepochs: 2.6, LloydIters: 25, Rounds: 3, Init: "forgy",
		Metal: clusterstudy.MetalStatus{Status: "not_run", Detail: "test"}, Runs: runs})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "summary.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStrataCompareMeasuresClustersAgainstTheCategoryAndAuditStrata(t *testing.T) {
	state, keys := studyState(t, 24, 4)
	// The first 12 kept postings read as a fit, the other 12 and the dropped ones do not.
	seedSonnet(t, state, keys, func(i int) string {
		if i < 12 {
			return "partial_fit"
		}
		return "mismatch_domain"
	})
	study := t.TempDir()
	mustCode(t, 0, "export", "pooled-vectors", "--out", study)
	keysPath := filepath.Join(study, pooledKeysFile)
	exported, err := clusterstudy.ReadKeys(keysPath)
	if err != nil {
		t.Fatal(err)
	}

	// Without a fit: the baselines and the k to fit.
	base := compareJSON(t, "--keys", keysPath)
	if base.Fit != nil || base.Funnel.Keys != len(exported) || base.Funnel.WithReading != len(exported) || base.Funnel.Positives != 12 {
		t.Fatalf("funnel %+v", base.Funnel)
	}
	var cat, aud baselineReport
	for _, b := range base.Baselines {
		switch b.Name {
		case baselineCategory:
			cat = b
		case baselineAudit:
			aud = b
		}
	}
	if cat.Cells != 2 || cat.N != len(exported) {
		t.Errorf("category baseline %+v", cat)
	}
	if aud.Cells < 2 || aud.N == 0 {
		t.Errorf("audit baseline %+v", aud)
	}
	if !containsInt(base.NeededK, 2) || !containsInt(base.NeededK, aud.Cells) {
		t.Errorf("needed k %v must hold the category cell count 2 and the audit cell count %d", base.NeededK, aud.Cells)
	}
	// Categories alternate independent of the reading: they explain little.
	if cat.Within.Plugin < 0.9*cat.Within.Total {
		t.Errorf("category within %v vs total %v", cat.Within.Plugin, cat.Within.Total)
	}

	// Perfect clusters (they follow the reading) and clusters that follow the categories.
	perfect := make([]uint32, len(exported))
	byCat := make([]uint32, len(exported))
	for i, k := range exported {
		if strings.HasPrefix(k, "gh:k:") {
			var n int
			fmt.Sscanf(k, "gh:k:%d", &n)
			if n < 12 {
				perfect[i] = 1
			}
			byCat[i] = uint32(n % 2)
		}
	}
	runs := []clusterstudy.FitRun{
		{Algo: clusterstudy.AlgoEmeansCPU, K: 2, Seed: 1, Assign: "e1.assign", Centroids: "e1.cent", MSE: 0.1, NonEmpty: 2, LabelsMatchEngine: true, FitMS: []float64{3, 1, 2}},
		{Algo: clusterstudy.AlgoEmeansCPU, K: 2, Seed: 2, Assign: "e2.assign", Centroids: "e2.cent", MSE: 0.1, NonEmpty: 2, LabelsMatchEngine: true, FitMS: []float64{2}},
		{Algo: clusterstudy.AlgoLloyd, K: 2, Seed: 1, Assign: "l1.assign", Centroids: "l1.cent", MSE: 0.12, NonEmpty: 2, LabelsMatchEngine: true, FitMS: []float64{9, 8}},
		{Algo: clusterstudy.AlgoLloyd, K: 2, Seed: 2, Assign: "l2.assign", Centroids: "l2.cent", MSE: 0.12, NonEmpty: 2, LabelsMatchEngine: true, FitMS: []float64{9}},
	}
	labels := map[string][]uint32{"e1.assign": perfect, "e2.assign": perfect, "l1.assign": byCat, "l2.assign": byCat}
	sp := writeStudySummary(t, study, exported, runs, labels)
	r := compareJSON(t, "--keys", keysPath, "--summary", sp, "--resamples", "50")
	if r.Fit == nil || r.Fit.Verdict == nil {
		t.Fatalf("no verdict: %+v", r.Fit)
	}
	v := r.Fit.Verdict
	if !v.AdoptClusters || v.Emeans.Median < 0.9 {
		t.Errorf("clusters that sort the reading must clear the bar: %+v", v)
	}
	if v.LloydMatches || v.Lloyd.Median > 0.1 {
		t.Errorf("clusters that follow the categories must not match them: %+v", v)
	}
	if r.Fit.Metal.Status != "not_run" {
		t.Errorf("metal status %+v", r.Fit.Metal)
	}
	var seen int
	for _, run := range r.Fit.Runs {
		if run.Algo == clusterstudy.AlgoEmeansCPU && run.K == 2 && run.Seed == 1 {
			seen++
			if run.FitMSMin != 1 || run.FitMSMedian != 2 || !run.Against[baselineCategory].EqualCells {
				t.Errorf("run %+v", run)
			}
		}
	}
	if seen != 1 {
		t.Errorf("run e-means k=2 seed=1 seen %d times", seen)
	}
	md, _ := mustCode(t, 0, "strata", "compare", "--keys", keysPath, "--summary", sp, "--resamples", "20")
	for _, want := range []string{"Decision rule", "adopt clusters: true", "funnel:", "fit these k"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestStrataCompareRefusesWhatDoesNotLineUp(t *testing.T) {
	state, keys := studyState(t, 10, 0)
	seedSonnet(t, state, keys, func(i int) string { return []string{"partial_fit", "mismatch_domain"}[i%2] })
	study := t.TempDir()
	mustCode(t, 0, "export", "pooled-vectors", "--out", study)
	keysPath := filepath.Join(study, pooledKeysFile)
	exported, _ := clusterstudy.ReadKeys(keysPath)

	cases := map[string][]string{
		"no --keys":         {"strata", "compare"},
		"unknown subcmd":    {"strata", "bogus"},
		"resamples zero":    {"strata", "compare", "--keys", keysPath, "--resamples", "0"},
		"resamples too big": {"strata", "compare", "--keys", keysPath, "--resamples", "99999999"},
	}
	for name, args := range cases {
		if code, _, _ := do(t, args...); code != exitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, exitUsage)
		}
	}
	// A summary for another keys file is refused, never measured.
	other := append(append([]string(nil), exported[1:]...), "gh:zzz:1")
	runs := []clusterstudy.FitRun{{Algo: clusterstudy.AlgoEmeansCPU, K: 2, Seed: 1, Assign: "a.assign", Centroids: "a.cent", NonEmpty: 2, LabelsMatchEngine: true}}
	lab := make([]uint32, len(other))
	sp := writeStudySummary(t, t.TempDir(), other, runs, map[string][]uint32{"a.assign": lab})
	code, _, e := do(t, "strata", "compare", "--keys", keysPath, "--summary", sp)
	if code != exitRuntime || !strings.Contains(e, "another keys file") {
		t.Errorf("foreign summary: exit %d, %s", code, e)
	}
	// A truncated keys file is refused.
	raw, _ := os.ReadFile(keysPath)
	cut := filepath.Join(t.TempDir(), "cut.keys")
	if err := os.WriteFile(cut, raw[:len(raw)-3], 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, e := do(t, "strata", "compare", "--keys", cut); code != exitRuntime || !strings.Contains(e, "newline") {
		t.Errorf("cut keys: exit %d, %s", code, e)
	}
	// A summary with no e-means run at the category cell count cannot give the verdict.
	sum := clusterstudy.KeysSHA256(exported)
	runs2 := []clusterstudy.FitRun{{Algo: clusterstudy.AlgoEmeansCPU, K: 3, Seed: 1, Assign: "a.assign", Centroids: "a.cent", NonEmpty: 3, LabelsMatchEngine: true}}
	lab2 := make([]uint32, len(exported))
	lab2[1], lab2[2] = 1, 2
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.assign"), clusterstudy.EncodeAssignments(sum, 3, lab2), 0o600); err != nil {
		t.Fatal(err)
	}
	sp2 := writeStudySummary(t, dir, exported, runs2, map[string][]uint32{"a.assign": lab2})
	if code, _, e := do(t, "strata", "compare", "--keys", keysPath, "--summary", sp2); code != exitRuntime || !strings.Contains(e, "cannot be applied") {
		t.Errorf("no e-means at the category cell count: exit %d, %s", code, e)
	}
}

func TestStrataCompareWithoutTeacherReadingsIsAnErrorNotAZero(t *testing.T) {
	_, _ = studyState(t, 6, 0)
	study := t.TempDir()
	mustCode(t, 0, "export", "pooled-vectors", "--out", study)
	code, _, e := do(t, "strata", "compare", "--keys", filepath.Join(study, pooledKeysFile))
	if code != exitRuntime || !strings.Contains(e, "no target") {
		t.Errorf("exit %d: %s", code, e)
	}
}

func TestInsideDir(t *testing.T) {
	for _, tc := range []struct {
		path, dir string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b/../b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
		{"/x/y", "/a/b", false},
	} {
		if got := insideDir(tc.path, tc.dir); got != tc.want {
			t.Errorf("insideDir(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
		}
	}
}
