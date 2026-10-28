package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/joburl"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// scopedFixtureEnv is attackFixture on disk with a config whose description
// exclusion drops some of one company's unmarked postings, so the row's status
// and its company's learn view depend on text the scoped read must have loaded.
func scopedFixtureEnv(t *testing.T) (*env, ledger.Snapshot) {
	t.Helper()
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	snap, _ := attackFixture()
	e := scaleState(t, snap)
	e.configPath = filepath.Join(t.TempDir(), "config.json")
	cfg0 := config.Default()
	cfg0.TargetSummary = "Platform engineer: Go, Rust, CUDA, Terraform and Kubernetes."
	cfg0.Exclude = config.Exclude{Descriptions: []string{"terraform and go every day"}}
	writeConfig(t, e.configPath, cfg0)
	if _, err := e.loadConfig(); err != nil {
		t.Fatalf("config does not load: %v", err)
	}
	return e, snap
}

func runJSON(t *testing.T, e *env, f func([]string) error, args ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	e.stdout, e.stderr = &buf, io.Discard
	if err := f(append([]string{"--json"}, args...)); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, buf.String())
	}
	return buf.Bytes()
}

// The row commands read the ledger with descriptions left out for every other
// company. Their whole replies must equal what the whole-ledger path builds
// (ReadSnapshot, every exclusion applied, the full insight), for every record.
func TestScopedReadRowsAreByteIdenticalToTheFullPath(t *testing.T) {
	e, snap := scopedFixtureEnv(t)
	// Marks change the company context and the row; write a few through the command.
	for _, i := range []int{1, 40, 100, 150} {
		runJSON(t, e, e.jobsMark, string(snap.Records[i].Key), []string{"saved", "hidden", "applied", "saved"}[i%4])
	}

	full, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := marks.Read(marks.Dir(e.stateDir))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := e.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	hideExcluded(full.Records, m, cfg.Exclude)
	in, err := e.newInsight(jobinsight.DefaultScoreHalfLifeDays, full, m)
	if err != nil {
		t.Fatal(err)
	}
	dropped := 0
	for _, r := range full.Records {
		if r.Status == posting.Dropped {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("the exclusion drops nothing, so the test would prove nothing about it")
	}

	for _, r := range full.Records {
		// show
		want := jobview.ToJob(r, m[r.Key])
		in.Enrich(&want, r, showKeywords)
		want.Description = jobview.CleanText(r.Posting.Description, jobview.DescRunes, true)
		var got jobview.Job
		if err := json.Unmarshal(runJSON(t, e, e.jobsShow, string(r.Key)), &got); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		if !bytes.Equal(a, b) {
			t.Errorf("show %s differs from the full path\nfull: %s\ngot:  %s", r.Key, a, b)
		}
		if r.Posting.Description != "" && got.Description == "" {
			t.Errorf("show %s exposed an empty description", r.Key)
		}

		// find
		wantFind := matchPageURL(full, m, mustNormalize(t, r.Posting.URL))
		if wantFind.Job == nil {
			t.Fatalf("%s: oracle finds no job for its own url", r.Key)
		}
		fr, _ := findRecord(full, wantFind.Job.Key)
		j := jobview.ToJob(fr, m[fr.Key])
		in.Enrich(&j, fr, 0)
		wantFind.Job.Row = j.Row
		wantFind.Job.RepeatApply = j.RepeatApply
		wantFind.Ledger, wantFind.Records = filepath.Join(e.stateDir, "ledger"), len(full.Records)
		var gotFind jobFindJSON
		if err := json.Unmarshal(runJSON(t, e, e.jobsFind, "--url", r.Posting.URL), &gotFind); err != nil {
			t.Fatal(err)
		}
		a, _ = json.Marshal(wantFind)
		b, _ = json.Marshal(gotFind)
		if !bytes.Equal(a, b) {
			t.Errorf("find %s differs from the full path\nfull: %s\ngot:  %s", r.Key, a, b)
		}
	}

	// mark: the reply is the row show gives for the same key afterwards.
	for _, i := range []int{0, 7, 120, len(full.Records) - 1} {
		k := string(full.Records[i].Key)
		var mj, sj jobview.Job
		if err := json.Unmarshal(runJSON(t, e, e.jobsMark, k, "saved"), &mj); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(runJSON(t, e, e.jobsShow, k), &sj); err != nil {
			t.Fatal(err)
		}
		sj.Description = ""
		a, _ := json.Marshal(mj)
		b, _ := json.Marshal(sj)
		if !bytes.Equal(a, b) {
			t.Errorf("mark %s differs from show\nmark: %s\nshow: %s", k, a, b)
		}
	}
}

// Descriptions are left out only for records outside the row's company, and no
// consumer can mistake such a record for a whole one: it is marked BodyEvicted
// with no text, the row itself is never one, and the row accessor refuses a
// row that is.
func TestRowScopeLeavesBodiesOutOnlyOutsideTheRowsCompany(t *testing.T) {
	e, snap := scopedFixtureEnv(t)
	dir := filepath.Join(e.stateDir, "ledger")
	whole, err := ledger.ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{0, 1, 40, 100, 108, len(snap.Records) - 1, len(snap.Records) - 3} {
		row := snap.Records[i]
		co := companyKey(row.Posting.Company)
		got, err := ledger.ReadSnapshotScoped(dir, rowScope(func(ledger.Snapshot) (posting.Key, bool) { return row.Key, true }))
		if err != nil {
			t.Fatal(err)
		}
		left := 0
		for j, r := range got.Records {
			w := whole.Records[j]
			if r.BodyEvicted {
				left++
				if companyKey(r.Posting.Company) == co && co != "" {
					t.Errorf("row %s: %s is the row's company but its body was left out", row.Key, r.Key)
				}
				if r.Posting.Description != "" {
					t.Errorf("%s: left out but carries text", r.Key)
				}
				if w.Posting.Description == "" {
					t.Errorf("%s: no body to leave out but marked", r.Key)
				}
				continue
			}
			if r.Posting.Description != w.Posting.Description {
				t.Errorf("%s: body differs from the whole read", r.Key)
			}
		}
		if co != "" && left == 0 {
			t.Errorf("row %s: nothing was left out, so the scoped read saves nothing", row.Key)
		}
		if co == "" && left != 0 {
			t.Errorf("row %s has no company; every body is needed but %d were left out", row.Key, left)
		}
		if _, ok, err := rowRecord(got, row.Key); err != nil || !ok {
			t.Errorf("row %s: rowRecord %v %v", row.Key, ok, err)
		}
	}

	// A scope that loads nothing must not yield a usable row.
	none, err := ledger.ReadSnapshotScoped(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := rowRecord(none, snap.Records[0].Key); err == nil {
		t.Error("rowRecord returned a record whose body was left out")
	}
	// An unknown key loads nothing and is reported as missing, not as a row.
	miss, err := ledger.ReadSnapshotScoped(dir, rowScope(func(ledger.Snapshot) (posting.Key, bool) { return "gh:nope:1", true }))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := rowRecord(miss, "gh:nope:1"); ok || err != nil {
		t.Errorf("unknown key: ok=%v err=%v", ok, err)
	}
	// And a ledger that lost its file is an error, not an empty answer.
	if err := os.Remove(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	e.stdout = io.Discard
	if err := e.jobsShow([]string{"--json", string(snap.Records[0].Key)}); err == nil {
		t.Error("show on a missing ledger succeeded")
	}
}

func mustNormalize(t *testing.T, u string) string {
	t.Helper()
	n, err := joburl.Normalize(u)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
