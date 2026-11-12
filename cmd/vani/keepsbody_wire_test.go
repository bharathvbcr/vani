package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// keptLedger is a ledger, reopened so closed bodies are evicted, holding
// one live and one dropped posting stored without pay lines or a form.
func keptLedger(t *testing.T, live, dropped posting.Posting) *ledger.File {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []ledger.Event{
		{V: 1, At: jobsT0, Key: live.Key, Status: posting.Live, Posting: &live},
		{V: 1, At: jobsT0, Key: dropped.Key, Status: posting.Live, Posting: &dropped},
		{V: 1, At: jobsT0, Key: dropped.Key, Status: posting.Dropped, Reason: "test"},
	} {
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	if l, err = ledger.Open(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if r, _ := l.Get(dropped.Key); !pipeline.KeepsStoredBody(r) {
		t.Fatalf("setup: the dropped record is %+v", r)
	}
	return l
}

// asker records the ids a fake board was asked about.
type asker struct {
	mu  sync.Mutex
	ids []string
}

func (a *asker) add(id string) {
	a.mu.Lock()
	a.ids = append(a.ids, id)
	a.mu.Unlock()
}

func (a *asker) got() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.ids, ",")
}

// The run's Greenhouse source asks no pay for a posting the engine dropped
// while its text is unchanged, and still asks for a live one. Before the
// wiring, the dropped Anthropic postings were asked every run and spent the
// run's whole pay budget.
func TestRunGreenhouseSourceSkipsPayForDroppedPostings(t *testing.T) {
	stored := func(id string) posting.Posting {
		k := posting.Key("gh:acme:" + id)
		return posting.Posting{Key: k, ATS: posting.Greenhouse, Company: "Acme", Title: "T", Description: "Body " + id + "."}
	}
	l := keptLedger(t, stored("1"), stored("2"))
	var pay asker
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case r.URL == "https://boards-api.greenhouse.io/v1/boards/acme/jobs?content=true":
			return srctest.Reply{Status: 200, Body: []byte(`{"jobs":[` +
				`{"id":1,"title":"T","absolute_url":"https://x.test/1","content":"Body 1."},` +
				`{"id":2,"title":"T","absolute_url":"https://x.test/2","content":"Body 2."}]}`)}, true
		case strings.HasSuffix(r.URL, "?pay_transparency=true"):
			pay.add(strings.TrimSuffix(r.URL[strings.LastIndex(r.URL, "/")+1:], "?pay_transparency=true"))
			return srctest.Reply{Status: 200, Body: []byte(`{"pay_input_ranges":[{"min_cents":10000000,"max_cents":12000000,"currency_type":"USD","title":"Annual"}]}`)}, true
		}
		return srctest.Reply{}, false
	})
	w := &wired{led: l, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	src, err := w.greenhouseSource(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.List(context.Background(), sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"})
	if err != nil || len(got) != 2 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if pay.got() != "1" {
		t.Fatalf("pay asked for %q: want the live posting 1 only", pay.got())
	}
	if got[1].Description != "Body 2." {
		t.Fatalf("dropped posting's description %q, want its stored body", got[1].Description)
	}
}

// The run's Ashby source reads no form for a posting the engine dropped while
// its text is unchanged, and still reads the live one's.
func TestRunAshbySourceSkipsFormsForDroppedPostings(t *testing.T) {
	const list = `{"jobs":[` +
		`{"id":"a","title":"T a","jobUrl":"https://x.test/a","isListed":true,"descriptionPlain":"text of a"},` +
		`{"id":"b","title":"T b","jobUrl":"https://x.test/b","isListed":true,"descriptionPlain":"text of b"}]}`
	var forms asker
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch {
		case r.URL == "https://api.ashbyhq.com/posting-api/job-board/acme?includeCompensation=true":
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		case strings.HasPrefix(r.URL, "https://jobs.ashbyhq.com/api/non-user-graphql?op=ApiJobPosting"):
			var q struct {
				Variables struct {
					ID string `json:"jobPostingId"`
				} `json:"variables"`
			}
			if json.Unmarshal(r.Body, &q) != nil {
				return srctest.Reply{}, false
			}
			forms.add(q.Variables.ID)
			return srctest.Reply{Status: 200, Body: []byte(`{"data":{"jobPosting":{"id":"` + q.Variables.ID +
				`","applicationForm":{"sections":[{"fieldEntries":[{"isRequired":true,"field":{"path":"q","title":"Q","type":"String"}}]}]}}}}`)}, true
		}
		return srctest.Reply{}, false
	})
	b := sources.Board{ATS: posting.Ashby, Token: "acme", Company: "Acme"}

	// What the board lists, as an earlier run stored it before any form was read.
	first := &wired{led: keptLedger(t, posting.Posting{Key: "ab:acme:x"}, posting.Posting{Key: "ab:acme:y"}), warn: &bytes.Buffer{}}
	src, err := first.ashbySource(srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		if strings.Contains(r.URL, "posting-api") {
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		}
		return srctest.Reply{Status: 500}, true
	}), pipeline.DropPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := src.List(context.Background(), b)
	if err != nil || len(listed) != 2 {
		t.Fatalf("%d postings, %v", len(listed), err)
	}
	for i := range listed {
		listed[i].Form = nil
	}

	w := &wired{led: keptLedger(t, listed[0], listed[1]), warn: &bytes.Buffer{}}
	src, err = w.ashbySource(d, pipeline.DropPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.List(context.Background(), b)
	if err != nil || len(got) != 2 {
		t.Fatalf("%d postings, %v", len(got), err)
	}
	if forms.got() != "a" {
		t.Fatalf("forms read for %q: want the live posting a only", forms.got())
	}
}
