package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// attackFixture crosses the boundaries the scoped pass must group exactly as
// the full pass: a boilerplate term shared by two companies (and absent from a
// third), NFC/NFD and case/space/suffix spellings of one name, an empty and a
// whitespace-only company that each hold enough postings to learn a blurb, and
// dead, hidden and empty-description records.
func attackFixture() (ledger.Snapshot, map[posting.Key]marks.Entry) {
	var snap ledger.Snapshot
	n := 0
	add := func(company, title, desc string, st posting.Status) posting.Key {
		n++
		k := posting.Key(fmt.Sprintf("gh:atk:%04d", n))
		snap.Records = append(snap.Records, ledger.Record{
			Key: k, Status: st, FirstSeen: jobsT0.AddDate(0, 0, -n),
			Posting: posting.Posting{Key: k, Company: company, Title: title, Description: desc,
				URL: fmt.Sprintf("https://boards.greenhouse.io/x/jobs/%04d", n)},
			Annotations: map[string]string{"triage.role_fit": "partial_fit", ledger.AnnMissing: "Kubernetes (weak)"},
		})
		return k
	}
	tech := []string{"Go", "Rust", "Python", "CUDA", "PyTorch", "TensorRT", "SQL", "Terraform", "Spark"}
	shared := "Our platform runs Kubernetes and Docker. "
	groups := []struct {
		name  string
		names []string
	}{
		{"cafe", []string{"Café Labs", "Café Labs", "CAFÉ LABS", " café labs "}},
		{"globex", []string{"Globex Inc.", "Globex Inc", "globex, inc.", "GLOBEX INC. "}},
		{"empty", []string{"", "   ", "\t"}},
		{"wired", []string{"Wired", "wired", " WIRED"}},
	}
	for _, g := range groups {
		for i := range 36 {
			add(g.names[i%len(g.names)], "Engineer "+g.name, shared+"You use "+tech[i%len(tech)]+" and "+tech[(i*2+1)%len(tech)]+".", posting.Digested)
		}
	}
	for i := range 12 {
		add("Initech", "Platform Engineer", "Terraform and "+tech[i%len(tech)]+" every day.", posting.Digested)
	}
	for i := range 2 {
		add("Tiny", "Eng", shared+tech[i], posting.Digested)
	}
	add("Solo", "Engineer", "Kubernetes", posting.Digested)
	add("Wired", "Empty", "", posting.Digested)
	dead := add("Wired", "Closed", shared+"Rust", posting.Dead)
	deadHidden := add("Globex Inc.", "Closed marked", shared+"Rust", posting.Dead)
	m := map[posting.Key]marks.Entry{
		dead:                  {Mark: marks.Saved}, // a marked dead record is in the learn pass
		deadHidden:            {Mark: marks.Hidden},
		snap.Records[1].Key:   {Mark: marks.Applied},
		snap.Records[40].Key:  {Mark: marks.Hidden},
		snap.Records[100].Key: {Mark: marks.Saved},
	}
	return snap, m
}

func TestRowInsightAttackParity(t *testing.T) {
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	snap, m := attackFixture()
	for _, cfg := range []config.Config{
		{TargetSummary: "Platform engineer: Go, Rust, CUDA, Terraform and Kubernetes."},
		{},
	} {
		full, err := newInsightFor(cfg, nil, t.TempDir(), 14, snap, m)
		if err != nil {
			t.Fatal(err)
		}
		if len(full.boiler) < 3 {
			t.Fatalf("fixture learns %d blurbs", len(full.boiler))
		}
		for _, r := range snap.Records {
			one, err := newInsightScoped(cfg, nil, t.TempDir(), 14, snap, m, companyKey(r.Posting.Company))
			if err != nil {
				t.Fatal(err)
			}
			var enc [2][]byte
			for i, in := range []*insight{full, one} {
				j := jobview.ToJob(r, m[r.Key])
				hits := in.Enrich(&j, r, showKeywords)
				enc[i], _ = json.Marshal(struct {
					J jobview.Job
					H any
				}{j, hits})
			}
			if !bytes.Equal(enc[0], enc[1]) {
				t.Errorf("%s (%q) differs\nfull: %s\none:  %s", r.Key, r.Posting.Company, enc[0], enc[1])
			}
		}
	}
}

// jobs show, jobs mark, jobs find and jobs list agree on the same key, for
// records of every shape, through the real commands on a ledger on disk.
func TestRowInsightAttackCommandsAgree(t *testing.T) {
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	old := now
	now = func() time.Time { return jobsT0 }
	t.Cleanup(func() { now = old })
	snap, _ := attackFixture()
	e := scaleState(t, snap)
	run := func(f func([]string) error, args ...string) []byte {
		var buf bytes.Buffer
		e.stdout, e.stderr = &buf, io.Discard
		if err := f(append([]string{"--json"}, args...)); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, buf.String())
		}
		return buf.Bytes()
	}
	var list jobview.List
	if err := json.Unmarshal(run(e.jobsList, "--view", "all", "--limit", "500", "--sort", "priority"), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Jobs) < 100 {
		t.Fatalf("list returned %d", len(list.Jobs))
	}
	for i, lj := range list.Jobs {
		if i%7 != 0 && i > 5 {
			continue
		}
		var sj jobview.Job
		if err := json.Unmarshal(run(e.jobsShow, string(lj.Key)), &sj); err != nil {
			t.Fatal(err)
		}
		rl, _ := json.Marshal(lj.Row)
		rs, _ := json.Marshal(sj.Row)
		if !bytes.Equal(rl, rs) {
			t.Errorf("%s list row != show row\nlist: %s\nshow: %s", lj.Key, rl, rs)
		}
		if lj.Priority != sj.Priority || lj.Relevance != sj.Relevance || fmt.Sprint(lj.ScoreFactors) != fmt.Sprint(sj.ScoreFactors) {
			t.Errorf("%s score list %v/%v show %v/%v", lj.Key, lj.Priority, lj.Relevance, sj.Priority, sj.Relevance)
		}
		var url string
		for _, x := range snap.Records {
			if x.Key == lj.Key {
				url = x.Posting.URL
			}
		}
		var fj struct {
			Job *struct {
				Row json.RawMessage `json:"row"`
			} `json:"job"`
		}
		raw := run(e.jobsFind, "--url", url)
		if err := json.Unmarshal(raw, &fj); err != nil || fj.Job == nil {
			t.Fatalf("find %s: %v %s", url, err, raw)
		}
		if rf := bytes.TrimSpace(fj.Job.Row); !bytes.Equal(rf, rs) {
			t.Errorf("%s find row != show row\nfind: %s\nshow: %s", lj.Key, rf, rs)
		}
	}
	if out := run(e.jobsFind, "--url", "https://boards.greenhouse.io/x/jobs/99999"); !strings.Contains(string(out), "99999") {
		t.Errorf("unknown url: %s", out)
	}
	e.stdout = io.Discard
	if err := e.jobsShow([]string{"--json", "gh:atk:9999"}); err == nil {
		t.Error("show of an unknown key succeeded")
	}
	// mark on a closed record, an empty description, an empty company and a
	// multi-spelling company returns the row show returns.
	for _, i := range []int{len(snap.Records) - 1, len(snap.Records) - 2, len(snap.Records) - 3, 108, 0} {
		k := string(snap.Records[i].Key)
		var mj, sj jobview.Job
		if err := json.Unmarshal(run(e.jobsMark, k, "saved"), &mj); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(run(e.jobsShow, k), &sj); err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(mj.Row)
		b, _ := json.Marshal(sj.Row)
		if !bytes.Equal(a, b) || mj.Priority != sj.Priority {
			t.Errorf("%s mark row != show row\nmark: %s\nshow: %s", k, a, b)
		}
	}
}
