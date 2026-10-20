package main

import (
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/jobcategory"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/jobview"
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
	jobview.Filter
	minSalary           string
	seniority           string
	category, folder    string
	company, notCompany string
	exclude             string
}

func addFilterFlags(fs *flag.FlagSet, f *jobsFilter) {
	fs.StringVar(&f.minSalary, "min-salary", "", "only jobs whose stated pay reaches this much a year in --currency (150000 or 150k); "+
		"jobs that state no pay stay unless --salary-listed, and jobs that state pay only in another currency are left out (no exchange rates)")
	fs.StringVar(&f.Currency, "currency", "USD", "the currency of --min-salary, the salary sort and the pay stats: "+strings.Join(jobinsight.PayCurrencies(), ", "))
	fs.BoolVar(&f.PayListed, "salary-listed", false, "only jobs whose text states pay")
	fs.StringVar(&f.seniority, "seniority", "", "only these seniority readings (comma list; none = no reading)")
	fs.StringVar(&f.category, "category", "", "only jobs in these category folders (comma list: "+strings.Join(jobcategory.OptionValues(), ", ")+"; none = no reading)")
	fs.StringVar(&f.folder, "folder", "", "alias for --category")
	fs.StringVar(&f.company, "company", "", "only these companies (comma list of exact names, any case)")
	fs.StringVar(&f.notCompany, "exclude-company", "", "leave out these companies (comma list of exact names, any case)")
	fs.StringVar(&f.exclude, "exclude", "", "leave out jobs whose title contains any of these words (comma list, any case)")
	fs.Float64Var(&f.MinRelevance, "min-relevance", 0, "only jobs whose relevance is at least this (0-100)")
}

// check validates and parses the flags.
func (f *jobsFilter) check(cmd string) error {
	if f.folder != "" && f.category == "" {
		f.category = f.folder
	}
	for name, v := range map[string]string{"min-salary": f.minSalary, "currency": f.Currency, "seniority": f.seniority,
		"category": f.category, "company": f.company, "exclude-company": f.notCompany, "exclude": f.exclude} {
		if len(v) > maxFilterFlagBytes || !utf8.ValidString(v) {
			return usagef("%s: --%s must be UTF-8 and at most %d bytes", cmd, name, maxFilterFlagBytes)
		}
	}
	f.Currency = strings.ToUpper(strings.TrimSpace(f.Currency))
	if jobinsight.PayStep(f.Currency) == 0 {
		return usagef("%s: --currency %q is not one of %s", cmd, f.Currency, strings.Join(jobinsight.PayCurrencies(), ", "))
	}
	v, err := parseSalary(f.minSalary)
	if err != nil {
		return usagef("%s: --min-salary %v", cmd, err)
	}
	f.MinPay = v
	if !(f.MinRelevance >= 0 && f.MinRelevance <= 100) {
		return usagef("%s: --min-relevance must be 0-100", cmd)
	}
	if f.Seniorities, err = optionSet(cmd, "seniority", "seniority", f.seniority); err != nil {
		return err
	}
	if f.Categories, err = optionSet(cmd, "category", "category", f.category); err != nil {
		return err
	}
	if f.Companies, err = nameSet(cmd, "company", f.company); err != nil {
		return err
	}
	if f.NotCompanies, err = nameSet(cmd, "exclude-company", f.notCompany); err != nil {
		return err
	}
	words, err := nameSet(cmd, "exclude", f.exclude)
	if err != nil {
		return err
	}
	f.ExcludeTerms = f.ExcludeTerms[:0]
	for w := range words {
		f.ExcludeTerms = append(f.ExcludeTerms, w)
	}
	sort.Strings(f.ExcludeTerms)
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

// --- pay stats ---

// payNote is a job's pay in cur (else its first stated pay) for text output,
// "USD 150k–180k/yr", or "" when it states none.
func payNote(j jobview.Job, cur string) string {
	p, ok := jobinsight.PayIn(j.Pay, cur)
	if !ok {
		if len(j.Pay) == 0 {
			return ""
		}
		p = j.Pay[0]
	}
	per := map[jobinsight.Period]string{jobinsight.Hour: "/hr", jobinsight.Day: "/day", jobinsight.Week: "/wk",
		jobinsight.Month: "/mo", jobinsight.Year: "/yr"}[p.Period]
	amount := jobinsight.ShortMoney(p.Min)
	if p.Max != p.Min {
		amount += "–" + jobinsight.ShortMoney(p.Max)
	}
	return p.Currency + " " + amount + per
}
