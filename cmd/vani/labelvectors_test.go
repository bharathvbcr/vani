package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	docs, unreadable, more := labelledDocs(recs, labels, has, text)
	if len(docs) != 1 || docs[0].Key != "gh:x:0" || unreadable != 1 || more != 0 {
		t.Fatalf("docs %+v unreadable %d more %d, want only gh:x:0 (1 is a skip, 2 has a vector, 3 is unreadable, 4 is unlabelled)", docs, unreadable, more)
	}

	// The cap: maxLabelEmbed sent, the rest counted for the next run.
	recs, labels = nil, map[posting.Key]eval.Entry{}
	for i := range maxLabelEmbed + 3 {
		k := posting.Key(fmt.Sprintf("gh:y:%04d", i))
		recs = append(recs, ledger.Record{Key: k, Posting: posting.Posting{Key: k, Title: "t", Description: "d"}})
		labels[k] = eval.Entry{Labels: map[string]string{eval.WouldApply: "no"}}
	}
	docs, _, more = labelledDocs(recs, labels, nil, text)
	if len(docs) != maxLabelEmbed || more != 3 {
		t.Fatalf("cap: %d sent, %d more", len(docs), more)
	}
}
