package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
)

// The warm embeds kept and marked postings only, in Score order, so a judged
// posting that was dropped, or kept below the budget, never had a vector and
// the taste column could not read it (31 of 32 judged postings, 2026-11-14).
// `vani semantic warm` now also embeds the owner's judged postings that have
// none, under the text the lookups hash (an evicted description read back),
// and only those: a skip, an unlabelled dropped posting and a posting already
// embedded are not sent, and a second warm sends nothing.
func TestSemanticWarmEmbedsJudgedPostings(t *testing.T) {
	_, state := semanticSetup(t)
	seeds := []insightSeed{
		{key: "gh:a:k1", company: "Acme", title: "Platform Engineer", status: posting.Explained, desc: "Run k8s clusters and golang services", posted: daysAgo(1), seenAgo: time.Hour},
		{key: "gh:a:k2", company: "Acme", title: "Account Executive", status: posting.Explained, desc: "Sell enterprise subscriptions", posted: daysAgo(30), seenAgo: time.Hour},
		{key: "gh:a:d1", company: "Acme", title: "Staff Engineer", status: posting.Dropped, desc: "Lead golang infrastructure work", posted: daysAgo(2), seenAgo: time.Hour},
		{key: "gh:a:d2", company: "Acme", title: "Manager", status: posting.Dropped, desc: "Manage the k8s team", posted: daysAgo(2), seenAgo: time.Hour},
		{key: "gh:a:d3", company: "Acme", title: "Director", status: posting.Dropped, desc: "Direct the org", posted: daysAgo(2), seenAgo: time.Hour},
	}
	seedInsight(t, state, seeds...)
	mustCode(t, 0, "audit", "answer", "--key", "gh:a:k2", "--verdict", "maybe", "--source", "decile:2", "--p-select", "0.5")
	mustCode(t, 0, "audit", "answer", "--key", "gh:a:d1", "--verdict", "no", "--source", "dropped:model:seniority", "--p-select", "0.5")
	mustCode(t, 0, "audit", "answer", "--key", "gh:a:d2", "--verdict", "skip", "--source", "dropped:model:seniority", "--p-select", "0.5")

	// A budget of one: the warm reaches the best kept posting only.
	_, stderr := mustCode(t, 0, "semantic", "warm", "--max", "1")
	if !strings.Contains(stderr, "labelled postings: scored 2") {
		t.Fatalf("the labelled step should embed k2 (kept, beyond the budget) and d1 (dropped):\n%s", stderr)
	}

	vr, err := semantic.OpenVectorReader(semanticDir(state))
	if err != nil {
		t.Fatal(err)
	}
	defer vr.Close()
	for _, s := range seeds {
		_, st := vr.Pooled(s.title, s.desc)
		want := s.key != "gh:a:d2" && s.key != "gh:a:d3" // a skip and an unlabelled drop are not embedded
		if (st == semantic.PoolOK) != want {
			t.Errorf("%s: vector status %v, want a vector %v", s.key, st, want)
		}
	}
	// The dropped posting is stored under its full text, not its title alone
	// (what hashing the evicted record as the snapshot holds it would give).
	if _, st := vr.Pooled("Staff Engineer", ""); st == semantic.PoolOK {
		t.Error("the dropped posting was embedded under its title alone")
	}

	// Nothing left to send: a second warm embeds no labelled posting.
	if _, stderr = mustCode(t, 0, "semantic", "warm", "--max", "1"); !strings.Contains(stderr, "labelled postings: scored 0") {
		t.Fatalf("second warm:\n%s", stderr)
	}

	// The taste column finds both judged postings' vectors. The dropped one is
	// held in the jobs snapshot without its description; hashing it as held
	// (the code before 2026-11-14) would count it never embedded.
	out, _ := mustCode(t, 0, "relevance", "--json")
	var rep relevanceReply
	decodeOne(t, out, &rep)
	if rep.Taste == nil || rep.Taste.NoVector != 0 || len(rep.Taste.WithoutVector) != 0 {
		t.Fatalf("taste column after the warm: %+v", rep.Taste)
	}
}

// labelledDocs sends only judged postings without a current vector whose
// text could be read, counts the unreadable ones, and caps one call.
func TestLabelledDocsLeaveOutSkipsKnownVectorsAndUnreadable(t *testing.T) {
	var recs []ledger.Record
	labels := map[posting.Key]eval.Entry{}
	for i, v := range []string{"yes", "skip", "no", "maybe", ""} {
		k := posting.Key(fmt.Sprintf("gh:x:%d", i))
		recs = append(recs, ledger.Record{Key: k, Posting: posting.Posting{Key: k, Title: fmt.Sprintf("T%d", i), Description: "d"}})
		if v != "" {
			labels[k] = eval.Entry{Labels: map[string]string{eval.WouldApply: v}}
		}
	}
	labels["gh:x:gone"] = eval.Entry{Labels: map[string]string{eval.WouldApply: "yes"}} // not in the ledger
	has := func(title, _ string) bool { return title == "T2" }
	text := func(r ledger.Record) (string, error) {
		if r.Key == "gh:x:3" {
			return "", ledger.ErrBodyUnavailable
		}
		return r.Posting.Description, nil
	}
	docs, hashes, unreadable, more := labelledDocs(recs, labels, has, text)
	if len(docs) != 1 || docs[0].Key != "gh:x:0" || unreadable != 1 || more != 0 {
		t.Fatalf("docs %+v unreadable %d more %d, want only gh:x:0 (1 is a skip, 2 has a vector, 3 is unreadable, 4 is unlabelled)", docs, unreadable, more)
	}
	// Every judged posting whose text was read is pinned, embedded now or before.
	if len(hashes) != 2 || !slices.Contains(hashes, semantic.TextHash("T0", "d")) || !slices.Contains(hashes, semantic.TextHash("T2", "d")) {
		t.Fatalf("pinned hashes %v, want T0's and T2's", hashes)
	}

	// The cap: maxLabelEmbed sent, the rest counted for the next run.
	recs, labels = nil, map[posting.Key]eval.Entry{}
	for i := range maxLabelEmbed + 3 {
		k := posting.Key(fmt.Sprintf("gh:y:%04d", i))
		recs = append(recs, ledger.Record{Key: k, Posting: posting.Posting{Key: k, Title: "t", Description: "d"}})
		labels[k] = eval.Entry{Labels: map[string]string{eval.WouldApply: "no"}}
	}
	docs, _, _, more = labelledDocs(recs, labels, nil, text)
	if len(docs) != maxLabelEmbed || more != 3 {
		t.Fatalf("cap: %d sent, %d more", len(docs), more)
	}
}

// pinLabelled ranks the pinned texts above any Score in a named live set,
// leaves "no live set" alone, never changes its argument, and pins nothing
// from a missing or malformed list.
func TestPinLabelled(t *testing.T) {
	state := t.TempDir()
	ranks := map[string]float64{"a": 0.9, "b": 0.1}
	if got := pinLabelled(ranks, state); !maps.Equal(got, ranks) {
		t.Fatalf("no pin list: %v", got)
	}
	if err := writeLabelledPins(state, []string{"b", "c"}); err != nil {
		t.Fatal(err)
	}
	got := pinLabelled(ranks, state)
	if got["a"] != 0.9 || !math.IsInf(got["b"], 1) || !math.IsInf(got["c"], 1) || len(got) != 3 {
		t.Fatalf("pinned %v", got)
	}
	if ranks["b"] != 0.1 || len(ranks) != 2 {
		t.Fatalf("the caller's map changed: %v", ranks)
	}
	if pinLabelled(nil, state) != nil {
		t.Fatal("nil (no live set) must stay nil")
	}
	if err := os.WriteFile(filepath.Join(semanticDir(state), labelledPinsName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := pinLabelled(ranks, state); !maps.Equal(got, ranks) {
		t.Fatalf("malformed pin list: %v", got)
	}
}

// The daily run embeds a judged posting the warm cannot reach (a closed one,
// its description evicted and read back through the ledger) and pins its text
// for every later warm's live set; an unjudged closed posting is neither.
func TestRunEmbedsAndPinsJudgedPostings(t *testing.T) {
	cfgPath, stateDir := semanticSetup(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	dir, keys := advClosedLedger(t, 2)
	l, err := ledger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s, err := eval.NewStore(filepath.Join(stateDir, "eval", "labels"), filepath.Join(stateDir, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(eval.Entry{V: 1, Key: keys[0], Labels: map[string]string{eval.WouldApply: "no"}, LabeledAt: jobsT0, Source: "dropped:model:role_fit", PSelect: 0.5}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	e := &env{configPath: cfgPath, stateDir: stateDir, stdout: &bytes.Buffer{}, stderr: &stderr}
	app := &App{Ledger: l, RunOnce: func(context.Context) error { return nil }}
	if _, err := e.runAndPublish(context.Background(), app, cfg, runOptions{}, &runRecord{}); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "labelled postings: scored 1") {
		t.Fatalf("the run did not embed the judged closed posting:\n%s", stderr.String())
	}
	full := ledger.Full(l, ledger.Record{Key: keys[0], BodyEvicted: true})
	want := semantic.TextHash(full.Posting.Title, full.Posting.Description)
	vr, err := semantic.OpenVectorReader(semanticDir(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer vr.Close()
	if _, st := vr.Pooled(full.Posting.Title, full.Posting.Description); st != semantic.PoolOK {
		t.Fatalf("judged closed posting: vector status %v", st)
	}
	other := ledger.Full(l, ledger.Record{Key: keys[1], BodyEvicted: true})
	if _, st := vr.Pooled(other.Posting.Title, other.Posting.Description); st == semantic.PoolOK {
		t.Fatal("an unjudged closed posting was embedded")
	}
	pinned := pinLabelled(map[string]float64{}, stateDir)
	if len(pinned) != 1 || !math.IsInf(pinned[want], 1) {
		t.Fatalf("pins %v, want only the judged posting's text", pinned)
	}
}
