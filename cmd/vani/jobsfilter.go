package main

import (
	"flag"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// The pay, seniority, company, title and relevance filters `vani jobs list`
// and `jobs stats` share, the salary sort and the pay stats. Pay is what
// jobinsight.ParsePay reads from the posting text: display-only like every
// other jobs filter. Nothing here writes the ledger or feeds an engine rule.

const (
	maxFilterFlagBytes = 1000
	maxFilterItems     = 50
	maxSalaryFlag      = 1e9
)

// jobsFilter is the part of jobsOpts this file owns.
type jobsFilter struct {
	minSalary, currency     string
	minPay                  float64 // a year's pay in currency; 0 is no floor
	payListed               bool
	seniority               string
	seniorities             map[string]bool
	company, notCompany     string
	companies, notCompanies map[string]bool
	exclude                 string
	excludeTerms            []string
	minRelevance            float64
}

func addFilterFlags(fs *flag.FlagSet, f *jobsFilter) {
	fs.StringVar(&f.minSalary, "min-salary", "", "only jobs whose stated pay reaches this much a year in --currency (150000 or 150k); "+
		"jobs that state no pay stay unless --salary-listed, and jobs that state pay only in another currency are left out (no exchange rates)")
	fs.StringVar(&f.currency, "currency", "USD", "the currency of --min-salary, the salary sort and the pay stats: "+strings.Join(jobinsight.PayCurrencies(), ", "))
	fs.BoolVar(&f.payListed, "salary-listed", false, "only jobs whose text states pay")
	fs.StringVar(&f.seniority, "seniority", "", "only these seniority readings (comma list; none = no reading)")
	fs.StringVar(&f.company, "company", "", "only these companies (comma list of exact names, any case)")
	fs.StringVar(&f.notCompany, "exclude-company", "", "leave out these companies (comma list of exact names, any case)")
	fs.StringVar(&f.exclude, "exclude", "", "leave out jobs whose title contains any of these words (comma list, any case)")
	fs.Float64Var(&f.minRelevance, "min-relevance", 0, "only jobs whose relevance is at least this (0-100)")
}

// check validates and parses the flags.
func (f *jobsFilter) check(cmd string) error {
	for name, v := range map[string]string{"min-salary": f.minSalary, "currency": f.currency, "seniority": f.seniority,
		"company": f.company, "exclude-company": f.notCompany, "exclude": f.exclude} {
		if len(v) > maxFilterFlagBytes || !utf8.ValidString(v) {
			return usagef("%s: --%s must be UTF-8 and at most %d bytes", cmd, name, maxFilterFlagBytes)
		}
	}
	f.currency = strings.ToUpper(strings.TrimSpace(f.currency))
	if jobinsight.PayStep(f.currency) == 0 {
		return usagef("%s: --currency %q is not one of %s", cmd, f.currency, strings.Join(jobinsight.PayCurrencies(), ", "))
	}
	v, err := parseSalary(f.minSalary)
	if err != nil {
		return usagef("%s: --min-salary %v", cmd, err)
	}
	f.minPay = v
	if !(f.minRelevance >= 0 && f.minRelevance <= 100) {
		return usagef("%s: --min-relevance must be 0-100", cmd)
	}
	if f.seniorities, err = optionSet(cmd, "seniority", "seniority", f.seniority); err != nil {
		return err
	}
	if f.companies, err = nameSet(cmd, "company", f.company); err != nil {
		return err
	}
	if f.notCompanies, err = nameSet(cmd, "exclude-company", f.notCompany); err != nil {
		return err
	}
	words, err := nameSet(cmd, "exclude", f.exclude)
	if err != nil {
		return err
	}
	f.excludeTerms = f.excludeTerms[:0]
	for w := range words {
		f.excludeTerms = append(f.excludeTerms, w)
	}
	sort.Strings(f.excludeTerms)
	return nil
}

// parseSalary reads "150000", "150,000", "150_000" or "150k". Empty is 0.
func parseSalary(raw string) (float64, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, nil
	}
	mult := 1.0
	if strings.HasSuffix(s, "k") {
		mult, s = 1000, strings.TrimSpace(strings.TrimSuffix(s, "k"))
	}
	s = strings.NewReplacer(",", "", "_", "").Replace(s)
	if s == "" || strings.Trim(s, "0123456789.") != "" {
		return 0, fmt.Errorf("%q is not an amount like 150000 or 150k", raw)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not an amount like 150000 or 150k", raw)
	}
	v *= mult
	if v < 0 || v > maxSalaryFlag {
		return 0, fmt.Errorf("must be 0-%g", float64(maxSalaryFlag))
	}
	return v, nil
}

// nameSet parses a comma list into lowercased, trimmed, non-empty items.
func nameSet(cmd, flagName, raw string) (map[string]bool, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	out := map[string]bool{}
	for _, v := range strings.Split(raw, ",") {
		if v = strings.ToLower(strings.Join(strings.Fields(v), " ")); v != "" {
			out[v] = true
		}
	}
	switch {
	case len(out) == 0:
		return nil, usagef("%s: --%s names nothing", cmd, flagName)
	case len(out) > maxFilterItems:
		return nil, usagef("%s: --%s names more than %d items", cmd, flagName, maxFilterItems)
	}
	return out, nil
}

// keep reports whether an enriched job passes the filters.
func (f *jobsFilter) keep(j *jobJSON) bool {
	if !inSet(f.seniorities, j.Triage["seniority"]) {
		return false
	}
	company := strings.ToLower(strings.Join(strings.Fields(j.Company), " "))
	if (f.companies != nil && !f.companies[company]) || f.notCompanies[company] {
		return false
	}
	if len(f.excludeTerms) > 0 {
		title := strings.ToLower(j.Title)
		for _, w := range f.excludeTerms {
			if strings.Contains(title, w) {
				return false
			}
		}
	}
	if f.minRelevance > 0 && j.Relevance < f.minRelevance {
		return false
	}
	if f.payListed && len(j.Pay) == 0 {
		return false
	}
	if f.minPay > 0 && len(j.Pay) > 0 {
		// Stated pay is compared in the chosen currency only; a job stating
		// pay only in another one cannot be compared, so it is left out.
		p, ok := jobinsight.PayIn(j.Pay, f.currency)
		if !ok || p.AnnualMax < f.minPay {
			return false
		}
	}
	return true
}

// payKey is a job's top annual pay in cur, or -1 when it states none in cur.
func payKey(j jobJSON, cur string) float64 {
	if p, ok := jobinsight.PayIn(j.Pay, cur); ok {
		return p.AnnualMax
	}
	return -1
}

// --- pay stats ---

type payBucketJSON struct {
	Label string  `json:"label"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"` // -1: no upper bound
	N     int     `json:"n"`
}

// payStatsJSON describes the pay the selected jobs state in Currency, by the
// middle of each job's annual range.
type payStatsJSON struct {
	Currency string `json:"currency"`
	Listed   int    `json:"listed"`   // state pay in Currency
	Other    int    `json:"other"`    // state pay only in another currency
	Unlisted int    `json:"unlisted"` // state no pay
	// Median is the median middle of the listed ranges; 0 when none.
	Median  float64         `json:"median"`
	Buckets []payBucketJSON `json:"buckets"`
}

const payBuckets = 8

func payStats(sel []selected, cur string) payStatsJSON {
	step := jobinsight.PayStep(cur)
	out := payStatsJSON{Currency: cur, Buckets: make([]payBucketJSON, 0, payBuckets)}
	// Buckets: under 2 steps, then one step wide, then the rest.
	for i := 0; i < payBuckets; i++ {
		lo, hi := float64(i+1)*step, float64(i+2)*step
		switch i {
		case 0:
			lo = 0
		case payBuckets - 1:
			hi = -1
		}
		label := "<" + shortMoney(hi)
		if i > 0 {
			label = shortMoney(lo) + "–" + shortMoney(hi)
		}
		if hi < 0 {
			label = "≥" + shortMoney(lo)
		}
		out.Buckets = append(out.Buckets, payBucketJSON{Label: label, Min: lo, Max: hi})
	}
	var mids []float64
	for _, s := range sel {
		p, ok := jobinsight.PayIn(s.job.Pay, cur)
		switch {
		case ok:
			out.Listed++
			mid := (p.AnnualMin + p.AnnualMax) / 2
			mids = append(mids, mid)
			for i := range out.Buckets {
				if b := out.Buckets[i]; b.Max < 0 || mid < b.Max {
					out.Buckets[i].N++
					break
				}
			}
		case len(s.job.Pay) > 0:
			out.Other++
		default:
			out.Unlisted++
		}
	}
	if n := len(mids); n > 0 {
		sort.Float64s(mids)
		out.Median = mids[n/2]
		if n%2 == 0 {
			out.Median = (mids[n/2-1] + mids[n/2]) / 2
		}
		out.Median = math.Round(out.Median)
	}
	return out
}

// shortMoney writes 150000 as 150k and 2500000 as 2.5M.
func shortMoney(v float64) string {
	switch {
	case v >= 1e6:
		return strconv.FormatFloat(math.Round(v/1e5)/10, 'f', -1, 64) + "M"
	case v >= 1e3:
		return strconv.FormatFloat(math.Round(v/1e2)/10, 'f', -1, 64) + "k"
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

// payNote is a job's pay in cur (else its first stated pay) for text output,
// "USD 150k–180k/yr", or "" when it states none.
func payNote(j jobJSON, cur string) string {
	p, ok := jobinsight.PayIn(j.Pay, cur)
	if !ok {
		if len(j.Pay) == 0 {
			return ""
		}
		p = j.Pay[0]
	}
	per := map[jobinsight.Period]string{jobinsight.Hour: "/hr", jobinsight.Day: "/day", jobinsight.Week: "/wk",
		jobinsight.Month: "/mo", jobinsight.Year: "/yr"}[p.Period]
	amount := shortMoney(p.Min)
	if p.Max != p.Min {
		amount += "–" + shortMoney(p.Max)
	}
	return p.Currency + " " + amount + per
}
