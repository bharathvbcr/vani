package main

import (
	"strings"
	"testing"
)

// Two places that share a run of wording get one warning, and it quotes the
// phrase where the shared run starts, every time. The phrase used to be
// whichever one a map range met first, so the same page warned differently
// from run to run.
func TestRepeatedPhraseWarningIsStable(t *testing.T) {
	c := onePage(
		&Bullet{ID: "b1", Text: "Designed a sharded vector index rebuild pipeline for nightly search refresh."},
		&Bullet{ID: "b2", Text: "Operated the sharded vector index rebuild pipeline across three regions."},
	)
	cf := &ClaimsFile{Claims: map[string]*ClaimView{}, Statements: map[string]*StatementView{}}
	const want = `phrase "sharded vector index rebuild" repeats in b1, b2; make the point once`
	for i := range 64 {
		R := &lintResult{}
		checkRepeats(c, cf, R)
		var got []string
		for _, w := range R.Warns {
			if strings.HasPrefix(w, "phrase ") {
				got = append(got, w)
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("run %d: phrase warnings %q, want exactly [%q]", i, got, want)
		}
	}
}

// Distinct pairs of places still each get their own warning, in a stable order.
func TestRepeatedPhraseOneWarningPerPair(t *testing.T) {
	c := onePage(
		&Bullet{ID: "a", Text: "Built streaming ingestion workers that replay failed batches safely."},
		&Bullet{ID: "b", Text: "Scaled streaming ingestion workers that replay failed batches nightly."},
		&Bullet{ID: "c", Text: "Wrote capacity planning dashboards for regional storage clusters."},
		&Bullet{ID: "d", Text: "Shipped capacity planning dashboards for regional storage clusters."},
	)
	cf := &ClaimsFile{Claims: map[string]*ClaimView{}, Statements: map[string]*StatementView{}}
	R := &lintResult{}
	checkRepeats(c, cf, R)
	var got []string
	for _, w := range R.Warns {
		if strings.HasPrefix(w, "phrase ") {
			got = append(got, w)
		}
	}
	want := []string{
		`phrase "capacity planning dashboards for" repeats in c, d; make the point once`,
		`phrase "streaming ingestion workers that" repeats in a, b; make the point once`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("phrase warnings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
