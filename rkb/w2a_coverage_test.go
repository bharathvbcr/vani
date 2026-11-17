package main

import (
	"strings"
	"testing"
)

func TestW2AEmptyAndTokenFreeBanks(t *testing.T) {
	c := &Composed{Composed: true, Header: Header{Name: "T"}}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{}}
	if u := claimUsage(c); len(u) != 0 {
		t.Errorf("empty resume usage: %v", u)
	}
	if p := renderResume(c, cf, true).R.coverage(cf); len(p) != 0 {
		t.Errorf("empty resume coverage: %v", p)
	}
}

func TestW2AUnregisteredTokenInField(t *testing.T) {
	c := &Composed{Composed: true, Header: Header{Name: "T", Headline: "{{clmGone}}"}}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{}}
	p := w2aJoin(renderResume(c, cf, true).R.coverage(cf))
	if !strings.Contains(p, "header headline") || !strings.Contains(p, "clmGone is not in claims.json") {
		t.Errorf("coverage = %q", p)
	}
}

// Every token the renderer resolves is in its record: the figures it counts
// as used are exactly the ones coverage and claimUsage see.
func TestW2ARecordMatchesResolvedTokens(t *testing.T) {
	c := w2aResume()
	_, vals := w2aEnv(t)
	cf := w2aClaims(c, vals)
	R := renderResume(c, cf, true).R
	seen := map[string]bool{}
	for _, u := range R.uses {
		for _, m := range tokenRe.FindAllStringSubmatch(u.Text, -1) {
			seen[m[1]] = true
		}
	}
	for id := range R.used {
		if !seen[id] {
			t.Errorf("the renderer resolved %s but did not record it", id)
		}
	}
	if len(R.coverage(cf)) != len(w2aTokenFields) {
		t.Errorf("coverage = %v", R.coverage(cf))
	}
}

func TestW2AStackAloneIsItsOwnField(t *testing.T) {
	c := &Composed{Composed: true, Header: Header{Name: "T"}, Sections: []*Section{{Title: "P", Kind: "entries",
		Entries: []*Entry{{ID: "e9", Title: "Plain", Stack: "{{clmS}} nodes"}}}}}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{"clmS": {Value: "9", DoubleVerified: true}}}
	if p := w2aJoin(renderResume(c, cf, true).R.coverage(cf)); !strings.Contains(p, "entry e9 stack: clmS") {
		t.Errorf("coverage = %q", p)
	}
}

func TestW2ADuplicateTokenInOneFieldReportedOnce(t *testing.T) {
	c := &Composed{Composed: true, Header: Header{Name: "T", Headline: "{{clmX}} and {{clmX}}"}}
	cf := &ClaimsFile{Claims: map[string]*ClaimView{"clmX": {Value: "1", DoubleVerified: true}}}
	if p := renderResume(c, cf, true).R.coverage(cf); len(p) != 1 {
		t.Errorf("coverage = %v", p)
	}
}
