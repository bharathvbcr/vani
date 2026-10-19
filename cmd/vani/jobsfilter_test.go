package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

type paySeed struct {
	key, company, title, seniority, rank, desc string
}

// paySeeds: descriptions in the shapes boards use, including the Lever and
// Ashby lines the adapters now write.
var paySeeds = []paySeed{
	{"gh:a:mid", "Acme", "Software Engineer", "mid", "90",
		"Build things. The United States base range for this position is $120,000 – $160,000 USD, plus equity."},
	{"gh:a:low", "Acme", "Support Engineer", "mid", "40", "Pay range: USD 90000 – 110000 per year\n\nHelp customers."},
	{"gh:b:none", "Globex", "Staff Engineer", "staff_plus", "80", "Build things. Compensation is competitive."},
	{"gh:b:gbp", "Globex", "Senior Engineer", "senior", "70", "Salary £95,000 - £120,000. Remote UK."},
	{"gh:c:hourly", "Initech", "Contract Engineer", "", "", "Pay rate: $70–$90/hr"},
	{"gh:c:mgr", "Initech", "Engineering Manager", "manager", "10", "Compensation: $210,000 – $280,313 • Offers Equity"},
}

func seedPay(t *testing.T, stateDir string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	for i, s := range paySeeds {
		p := posting.Posting{Key: posting.Key(s.key), ATS: "gh", Company: s.company, Title: s.title, Description: s.desc}
		ann := map[string]string{}
		if s.seniority != "" {
			ann["triage.seniority"] = s.seniority
		}
		if s.rank != "" {
			ann[ledger.AnnRank] = s.rank
		}
		e := ledger.Event{V: 1, At: jobsT0.Add(-time.Duration(i+1) * time.Hour), Key: p.Key, Status: posting.Explained, Posting: &p, Annotations: ann}
		if err := l.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJobsCarryPay(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedPay(t, state)
	l := listJSON(t, "--view", "all")
	if p := findJob(t, l, "gh:a:mid").Pay; len(p) != 1 || p[0].Currency != "USD" || p[0].AnnualMin != 120000 || p[0].AnnualMax != 160000 {
		t.Errorf("mid pay %+v", p)
	}
	if p := findJob(t, l, "gh:c:hourly").Pay; len(p) != 1 || p[0].Period != "hour" || p[0].AnnualMax != 90*2080 {
		t.Errorf("hourly pay %+v", p)
	}
	if p := findJob(t, l, "gh:b:none").Pay; p != nil {
		t.Errorf("no pay stated, got %+v", p)
	}
	o, _ := mustCode(t, 0, "jobs", "show", "--json", "gh:b:gbp")
	var j jobJSON
	if err := json.Unmarshal([]byte(o), &j); err != nil || len(j.Pay) != 1 || j.Pay[0].Currency != "GBP" {
		t.Errorf("show pay %q: %v", o, err)
	}
	// Text rows carry a short pay note.
	o, _ = mustCode(t, 0, "jobs", "list", "--view", "all")
	for _, want := range []string{"Software Engineer  USD 120k–160k/yr", "Contract Engineer  USD 70–90/hr", "Senior Engineer  GBP 95k–120k/yr"} {
		if !strings.Contains(o, want) {
			t.Errorf("text list lacks %q:\n%s", want, o)
		}
	}
}

func TestJobsPayFilters(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedPay(t, state)
	for _, c := range []struct {
		args []string
		want string
	}{
		// Stated pay below the floor goes; no stated pay stays; pay only in
		// another currency cannot be compared and goes.
		{[]string{"--min-salary", "150k"}, "gh:a:mid gh:b:none gh:c:hourly gh:c:mgr"},
		{[]string{"--min-salary", "150,000"}, "gh:a:mid gh:b:none gh:c:hourly gh:c:mgr"},
		{[]string{"--min-salary", "150k", "--salary-listed"}, "gh:a:mid gh:c:hourly gh:c:mgr"},
		{[]string{"--min-salary", "200000"}, "gh:b:none gh:c:mgr"},
		{[]string{"--min-salary", "100k", "--currency", "gbp"}, "gh:b:gbp gh:b:none"},
		{[]string{"--salary-listed"}, "gh:a:mid gh:a:low gh:b:gbp gh:c:hourly gh:c:mgr"},
		{[]string{"--min-salary", "0"}, "gh:a:mid gh:a:low gh:b:none gh:b:gbp gh:c:hourly gh:c:mgr"},
	} {
		got := keys(listJSON(t, append([]string{"--view", "all"}, c.args...)...))
		if !sameKeys(got, c.want) {
			t.Errorf("%q: %q, want %q", c.args, got, c.want)
		}
	}
}

func TestJobsSeniorityCompanyTitleRelevanceFilters(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedPay(t, state)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--seniority", "senior,staff_plus"}, "gh:b:none gh:b:gbp"},
		{[]string{"--seniority", "none"}, "gh:c:hourly"},
		{[]string{"--company", "acme"}, "gh:a:mid gh:a:low"},
		{[]string{"--company", " ACME , initech"}, "gh:a:mid gh:a:low gh:c:hourly gh:c:mgr"},
		{[]string{"--exclude-company", "Globex,Initech"}, "gh:a:mid gh:a:low"},
		{[]string{"--company", "acme", "--exclude-company", "acme"}, ""},
		{[]string{"--exclude", "manager,STAFF"}, "gh:a:mid gh:a:low gh:b:gbp gh:c:hourly"},
		{[]string{"--min-relevance", "50"}, "gh:a:mid gh:b:none gh:b:gbp"},
		{[]string{"--seniority", "mid", "--min-salary", "150k"}, "gh:a:mid"},
	} {
		got := keys(listJSON(t, append([]string{"--view", "all"}, c.args...)...))
		if !sameKeys(got, c.want) {
			t.Errorf("%q: %q, want %q", c.args, got, c.want)
		}
	}
}

func TestJobsSalarySort(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedPay(t, state)
	// mgr 280k, hourly 90*2080=187.2k, mid 160k, low 110k; then no USD pay
	// (gbp, none) by relevance.
	l := listJSON(t, "--view", "all", "--sort", "salary")
	if got := keys(l); got != "gh:c:mgr gh:c:hourly gh:a:mid gh:a:low gh:b:none gh:b:gbp" {
		t.Errorf("salary sort: %q", got)
	}
	if got := keys(listJSON(t, "--view", "all", "--sort", "salary", "--currency", "GBP")); !strings.HasPrefix(got, "gh:b:gbp ") {
		t.Errorf("salary sort in GBP: %q", got)
	}
}

func TestJobsPayStats(t *testing.T) {
	_, state := setup(t)
	fixedNow(t)
	seedPay(t, state)
	o, _ := mustCode(t, 0, "jobs", "stats", "--json", "--view", "all")
	var s jobsStatsJSON
	if err := json.Unmarshal([]byte(o), &s); err != nil {
		t.Fatalf("stats %q: %v", o, err)
	}
	p := s.Salary
	if p.Currency != "USD" || p.Listed != 4 || p.Other != 1 || p.Unlisted != 1 {
		t.Errorf("salary counts %+v", p)
	}
	// Middles: low 100k, mid 140k, hourly 166.4k, mgr 245.2k.
	n := map[string]int{}
	for _, b := range p.Buckets {
		n[b.Label] = b.N
	}
	if len(p.Buckets) != payBuckets || n["<100k"] != 0 || n["100k–150k"] != 2 || n["150k–200k"] != 1 || n["200k–250k"] != 1 || n["≥400k"] != 0 {
		t.Errorf("buckets %+v", p.Buckets)
	}
	if p.Median != 153200 {
		t.Errorf("median %v, want 153200", p.Median)
	}
	o, _ = mustCode(t, 0, "jobs", "stats", "--json", "--currency", "GBP", "--view", "all")
	if err := json.Unmarshal([]byte(o), &s); err != nil || s.Salary.Listed != 1 || s.Salary.Other != 4 || s.Salary.Buckets[0].Label != "<50k" {
		t.Errorf("GBP stats %+v: %v", s.Salary, err)
	}
	o, _ = mustCode(t, 0, "jobs", "stats", "--view", "all")
	if !strings.Contains(o, "pay (USD, middle of range): <100k 0, 100k–150k 2,") || !strings.Contains(o, "median 153.2k (stated 4, other currency 1, none 1)") {
		t.Errorf("text stats %q", o)
	}
	// With nothing stated the median is 0, not a guess.
	o, _ = mustCode(t, 0, "jobs", "stats", "--json", "--company", "globex", "--currency", "EUR")
	if err := json.Unmarshal([]byte(o), &s); err != nil || s.Salary.Listed != 0 || s.Salary.Median != 0 {
		t.Errorf("empty pay stats %+v: %v", s.Salary, err)
	}
}

func TestJobsFilterRefusals(t *testing.T) {
	_, state := setup(t)
	seedPay(t, state)
	many := strings.TrimSuffix(strings.Repeat("x,", maxFilterItems+1), ",")
	var distinct []string
	for i := 0; i <= maxFilterItems; i++ {
		distinct = append(distinct, "c"+strconv.Itoa(i))
	}
	for _, args := range [][]string{
		{"jobs", "list", "--min-salary", "lots"},
		{"jobs", "list", "--min-salary", "-5"},
		{"jobs", "list", "--min-salary", "2000000000"},
		{"jobs", "list", "--min-salary", "1e6"},
		{"jobs", "list", "--min-salary", "k"},
		{"jobs", "list", "--min-salary", "10m"},
		{"jobs", "list", "--currency", "XYZ"},
		{"jobs", "list", "--currency", ""},
		{"jobs", "list", "--min-relevance", "101"},
		{"jobs", "list", "--min-relevance", "-1"},
		{"jobs", "list", "--min-relevance", "NaN"},
		{"jobs", "list", "--seniority", "principal"},
		{"jobs", "list", "--company", " , ,"},
		{"jobs", "list", "--company", strings.Join(distinct, ",")},
		{"jobs", "list", "--exclude", strings.Repeat("y", maxFilterFlagBytes+1)},
		{"jobs", "list", "--exclude-company", "\xff"},
		{"jobs", "list", "--sort", "pay"},
		{"jobs", "stats", "--min-salary", "abc"},
	} {
		mustCode(t, 2, args...)
	}
	// Repeats collapse, so a long list of one name is within the cap.
	mustCode(t, 0, "jobs", "list", "--company", many)
}

func TestParseSalaryAndShortMoney(t *testing.T) {
	for in, want := range map[string]float64{"": 0, " 150k ": 150000, "150K": 150000, "150,000": 150000, "150_000": 150000, "1.5k": 1500, "0": 0} {
		if got, err := parseSalary(in); err != nil || got != want {
			t.Errorf("parseSalary(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"abc", "-1", "1e5", "$150k", "150kk", "1.2.3", "k", ","} {
		if _, err := parseSalary(in); err == nil {
			t.Errorf("parseSalary(%q) accepted", in)
		}
	}
	for v, want := range map[float64]string{0: "0", 52.5: "52.5", 999: "999", 1000: "1k", 153200: "153.2k", 2_500_000: "2.5M", 1_000_000: "1M"} {
		if got := shortMoney(v); got != want {
			t.Errorf("shortMoney(%v) = %q, want %q", v, got, want)
		}
	}
}
