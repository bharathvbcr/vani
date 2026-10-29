package main

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// A manual run's scope: `vani run-once --company/--state/--area/--remote/
// --or-remote` (and run-request, which takes the same flags) triage, score
// and explain only the postings at those companies and places. The rule is
// jobview.Scope, the one `jobs list` applies for its flags of the same names,
// so `jobs list --view pending` with them lists exactly the postings awaiting
// triage that the run would take. The cap (--advance, else advance_per_run)
// and the order apply within the scope; the postings outside it are untouched.

// maxScopeStates bounds --state: every USPS code, the territories and "none".
const maxScopeStates = 60

// scoped reports whether the options limit the advance phase to a scope.
func (o runOptions) scoped() bool { return !o.scope().IsZero() }

// scope is the options' advance scope as `jobs list` applies it.
func (o runOptions) scope() jobview.Scope {
	set := func(items []string) map[string]bool {
		if len(items) == 0 {
			return nil
		}
		m := make(map[string]bool, len(items))
		for _, s := range items {
			m[s] = true
		}
		return m
	}
	return jobview.Scope{Companies: set(o.Companies), States: set(o.States), Areas: set(o.Areas), Remote: o.Remote, OrRemote: o.OrRemote}
}

// scopeFlag is a comma-list scope flag (--company, --state, --area), folded
// by the parser `jobs list` uses for the flag of the same name. Repeating it
// adds to the list, which stays sorted and holds each item once.
type scopeFlag struct {
	dst   *[]string
	parse func(raw string) (map[string]bool, error)
}

func (f scopeFlag) String() string {
	if f.dst == nil {
		return ""
	}
	return strings.Join(*f.dst, ",")
}

func (f scopeFlag) Set(v string) error {
	set, err := f.parse(v)
	if err != nil {
		return err
	}
	if len(set) == 0 {
		return errors.New("names nothing")
	}
	for _, s := range *f.dst {
		set[s] = true
	}
	*f.dst = slices.Sorted(maps.Keys(set))
	return nil
}

// companySet parses --company as `jobs list` does (jobsFilter.check, then
// nameSet). The raw flag is checked before folding, because lower-casing turns
// invalid UTF-8 into replacement runes that would then pass as valid.
func companySet(cmd, raw string) (map[string]bool, error) {
	if len(raw) > maxFilterFlagBytes || !utf8.ValidString(raw) {
		return nil, usagef("%s: --company must be UTF-8 and at most %d bytes", cmd, maxFilterFlagBytes)
	}
	return nameSet(cmd, "company", raw)
}

// checkScope refuses a scope no run can apply. The daemon reads the options
// from a request file, so every item is checked to be in the folded form the
// flags write, not only when a flag parsed it.
func checkScope(o runOptions) error {
	if !o.scoped() {
		return nil
	}
	switch {
	case o.ListOnly:
		return usagef("--list-only skips the advance phase; --company, --state, --area and --remote have nothing to limit")
	case o.Remote && o.OrRemote:
		return usagef("--or-remote widens --state/--area to remote postings; --remote narrows to them; use one")
	case o.OrRemote && len(o.States) == 0 && len(o.Areas) == 0:
		return usagef("--or-remote needs --state or --area (alone it is --remote)")
	case len(o.Companies) > maxFilterItems:
		return usagef("--company names more than %d companies", maxFilterItems)
	case len(o.States) > maxScopeStates:
		return usagef("--state names more than %d states", maxScopeStates)
	case len(o.Areas) > maxAreas:
		return usagef("--area names more than %d areas", maxAreas)
	}
	for _, c := range o.Companies {
		if c == "" || len(c) > maxFilterFlagBytes || !utf8.ValidString(c) || jobview.CompanyKey(c) != c {
			return usagef("--company %q is not a company name as vani folds it", c)
		}
	}
	for _, s := range o.States {
		if code, ok := location.StateCode(s); s != "none" && (!ok || code != s) {
			return usagef("--state %q is not a US state code (or none)", s)
		}
	}
	for _, a := range o.Areas {
		if a == "" || len(a) > maxPlaceFlagBytes || !utf8.ValidString(a) || location.NormArea(a) != a {
			return usagef("--area %q is not an area as vani folds it", a)
		}
	}
	return nil
}

// scopeAdvance limits a run's advance phase to the options' scope: the
// postings waiting for a stage (pipeline.Advanceable) that are in it. It
// warns, in the run's log, when nothing waiting is in the scope or a named
// company has nothing waiting, so a misspelt name does not read as a run with
// nothing to do.
func scopeAdvance(p *pipeline.AdvancePlan, o runOptions, warn func(string, ...any)) {
	if !o.scoped() {
		return
	}
	sc := o.scope()
	p.Select = func(r ledger.Record) bool { return pipeline.Advanceable(r) && sc.Keep(r.Posting) }
	order := p.Order
	p.Order = func(todo, all []ledger.Record) {
		if warn != nil {
			warnScope(todo, o, warn)
		}
		if order != nil {
			order(todo, all)
		}
	}
}

// warnScope is scopeAdvance's warnings over the selected postings.
func warnScope(todo []ledger.Record, o runOptions, warn func(string, ...any)) {
	if len(todo) == 0 {
		warn("advance scope (%s): no posting waiting for triage, scoring or explaining is in it, so none is advanced", scopeWords(o))
		return
	}
	have := map[string]bool{}
	for _, r := range todo {
		have[jobview.CompanyKey(jobview.CleanText(r.Posting.Company, jobview.FieldRunes, false))] = true
	}
	for _, c := range o.Companies {
		if !have[c] {
			warn("advance scope: --company %q has no posting waiting for triage, scoring or explaining in the scope; "+
				"`vani jobs list --view pending` names the companies awaiting triage", c)
		}
	}
}

// scopeWords is the scope for a person: "postings at acme, initech in TX or
// remote", "remote postings". Empty for no scope.
func scopeWords(o runOptions) string {
	if !o.scoped() {
		return ""
	}
	s := "postings"
	if o.Remote {
		s = "remote postings"
	}
	if n := len(o.Companies); n > 0 {
		shown := o.Companies[:min(n, 5)]
		s += " at " + strings.Join(shown, ", ")
		if n > len(shown) {
			s += fmt.Sprintf(" and %d more companies", n-len(shown))
		}
	}
	if places := strings.Join(slices.Concat(o.States, o.Areas), ", "); places != "" {
		s += " in " + places
		if o.OrRemote {
			s += " or remote"
		}
	}
	return s
}
