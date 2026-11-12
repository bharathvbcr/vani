package samecompany

import (
	"fmt"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// groupsFixture is postings at a few companies, with relistings and
// applications spread across the apply window.
func groupsFixture() []ledger.Record {
	var recs []ledger.Record
	for i := range 120 {
		co := []string{"Acme", "Acme Inc.", "Beta", "Gamma LLC"}[i%4]
		loc := []string{"", "Austin, TX", "Remote", "New York, NY"}[i%3]
		recs = append(recs, rec(fmt.Sprintf("gh:b%d:%03d", i%4, i), co, fmt.Sprintf("Engineer %d", i%5), loc, t0.Add(-days(i))))
	}
	return recs
}

// One Groups paired with any marks and clock answers as a fresh New over the
// same records does: nothing in Groups depends on either.
func TestGroupsReusedAcrossMarksAndClocksAnswerAsNew(t *testing.T) {
	recs := groupsFixture()
	co := NewCompanies([]sources.Board{{ATS: posting.Greenhouse, Token: "b2", Company: "Beta", Domain: "beta.example"}})
	g := NewGroups(recs, co)
	markSets := []map[posting.Key]marks.Entry{
		nil,
		appliedAt("gh:b0:004", t0.Add(-days(3))),
		merge(appliedAt("gh:b0:004", t0.Add(-days(3))), appliedAt("gh:b1:009", t0.Add(-days(59))), appliedAt("gh:b2:010", t0.Add(-days(1)))),
	}
	clocks := []time.Time{t0, t0.Add(days(1)), t0.Add(days(58)), t0.Add(days(61)), t0.Add(-days(30))}
	for mi, m := range markSets {
		for ci, now := range clocks {
			kept, fresh := g.Index(m, now), New(recs, m, co, now)
			for _, r := range recs {
				ga, gok := kept.RepeatApply(r.Key, r.Posting.Company)
				fa, fok := fresh.RepeatApply(r.Key, r.Posting.Company)
				if ga != fa || gok != fok {
					t.Fatalf("marks %d clock %d %s: RepeatApply kept %+v %v, fresh %+v %v", mi, ci, r.Key, ga, gok, fa, fok)
				}
				gp, gok := kept.Repost(r)
				fp, fok := fresh.Repost(r)
				if gp != fp || gok != fok {
					t.Fatalf("marks %d clock %d %s: Repost kept %+v %v, fresh %+v %v", mi, ci, r.Key, gp, gok, fp, fok)
				}
			}
		}
	}
	// The role groups were built once each, by the first pairing that asked.
	built := g.Built()
	g.Index(markSets[2], t0).Repost(recs[0])
	if built == 0 || g.Built() != built {
		t.Fatalf("role groups built %d, then %d after another pairing: a kept Groups rebuilt them", built, g.Built())
	}
}

// Groups keeps copies of the fields it reads, not the records: a caller may
// change or drop its records (a snapshot with loaded descriptions) and the
// kept Groups answers as before.
func TestGroupsDoNotReadTheRecordsAfterBuilding(t *testing.T) {
	recs := groupsFixture()
	g := NewGroups(recs, nil)
	m := appliedAt("gh:b0:004", t0.Add(-days(3)))
	want := New(groupsFixture(), m, nil, t0)
	for i := range recs {
		recs[i].Posting.Title, recs[i].Posting.Company, recs[i].Posting.Location = "changed", "changed", "changed"
		recs[i].FirstSeen = time.Time{}
	}
	ix := g.Index(m, t0)
	for _, r := range groupsFixture() {
		gp, gok := ix.Repost(r)
		wp, wok := want.Repost(r)
		ga, aok := ix.RepeatApply(r.Key, r.Posting.Company)
		wa, wak := want.RepeatApply(r.Key, r.Posting.Company)
		if gp != wp || gok != wok || ga != wa || aok != wak {
			t.Fatalf("%s: answers changed after the caller's records did", r.Key)
		}
	}
}

// Groups files a posting's place under (location, remote) alone, so
// pipeline.LooseLocation must read no other field of a posting. If it starts
// to, this fails and Groups must keep that field too.
func TestLooseLocationReadsOnlyLocationAndRemote(t *testing.T) {
	for _, loc := range []string{"", "Austin, TX", "Remote - US", "Berlin; Paris", "N/A", "San Francisco, CA or Remote"} {
		for _, remote := range []bool{false, true} {
			full := posting.Posting{Key: "gh:x:1", Company: "Acme", Title: "Remote Engineer, Austin",
				Description: "Work from Berlin or remote anywhere.", Department: "Remote", Location: loc, Remote: remote,
				URL: "https://example.com/remote"}
			if got, want := locationOf(loc, remote), pipeline.LooseLocation(full); (loc != "" || remote) && got != want {
				t.Fatalf("location %q remote %v: fields-only %q, whole posting %q", loc, remote, got, want)
			}
		}
	}
}

func TestCompaniesKey(t *testing.T) {
	a := []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme", Domain: "acme.example"},
		{ATS: posting.Lever, Token: "beta", Company: "Beta", Domain: "beta.example"},
	}
	b := []sources.Board{a[1], a[0], {ATS: posting.Lever, Token: "plain", Company: "Plain"}} // reordered, plus a board with no domain
	if NewCompanies(a).Key() != NewCompanies(b).Key() {
		t.Fatal("boards that resolve alike have different keys")
	}
	if (*Companies)(nil).Key() != NewCompanies(nil).Key() {
		t.Fatal("no Companies and no domains resolve alike but have different keys")
	}
	c := []sources.Board{a[0], {ATS: posting.Lever, Token: "beta", Company: "Beta", Domain: "beta2.example"}}
	if NewCompanies(a).Key() == NewCompanies(c).Key() {
		t.Fatal("a changed domain kept the key")
	}
	d := []sources.Board{a[0], {ATS: posting.Lever, Token: "beta", Company: "Beta Two", Domain: "beta.example"}}
	if NewCompanies(a).Key() == NewCompanies(d).Key() {
		t.Fatal("a renamed domain board kept the key")
	}
}
