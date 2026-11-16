package main

import (
	"errors"
	"strings"
	"testing"
)

// A strict fit whose bullets could not be measured did not run its orphan and
// line-length checks, so it must not exit 0. A non-strict fit, and a page that
// already failed on its own, keep their status and say why the metrics are
// missing.
func TestJudgeBulletsUnmeasuredIsNeverAPass(t *testing.T) {
	cfg.Rules.OrphanRatio, cfg.Rules.MaxLines = 0.3, 3
	merr := errors.New("no measurement in the page Chrome --dump-dom returned")

	rep := FitReport{Advice: "OK: 2 page(s)."}
	judgeBullets(&rep, nil, merr, true)
	if rep.Status != 6 || !strings.Contains(rep.Advice, "UNMEASURED") || !strings.Contains(rep.Advice, "no measurement") {
		t.Errorf("strict, unmeasured: status %d advice %q", rep.Status, rep.Advice)
	}

	rep = FitReport{Advice: "OK: 2 page(s)."}
	judgeBullets(&rep, nil, merr, false)
	if rep.Status != 0 || !strings.Contains(rep.Advice, "metrics unavailable") {
		t.Errorf("not strict, unmeasured: status %d advice %q", rep.Status, rep.Advice)
	}

	rep = FitReport{Status: 3, Advice: "SHORT: add content."}
	judgeBullets(&rep, nil, merr, true)
	if rep.Status != 3 {
		t.Errorf("an earlier verdict was replaced: status %d", rep.Status)
	}

	rep = FitReport{Advice: "OK: 2 page(s)."}
	judgeBullets(&rep, []bulletMetric{{ID: "a", Lines: 2, LastLineFill: 0.1}, {ID: "b", Lines: 1, LastLineFill: 0.9}}, nil, true)
	if rep.Status != 5 || len(rep.Orphans) != 1 || rep.Orphans[0].ID != "a" || len(rep.Bullets) != 2 {
		t.Errorf("strict, orphan: %+v", rep)
	}

	rep = FitReport{Advice: "OK: 2 page(s)."}
	judgeBullets(&rep, []bulletMetric{{ID: "b", Lines: 1, LastLineFill: 0.9}}, nil, true)
	if rep.Status != 0 {
		t.Errorf("strict, clean page: status %d advice %q", rep.Status, rep.Advice)
	}
}

// The template slices each bullet's text to 70 UTF-16 units, which can split
// a surrogate pair; JSON.stringify then writes a lone surrogate escape. The
// metrics must still be read (the text is only a label), and a measurement
// that cannot be read must say why instead of coming back empty.
func TestParseMeasure(t *testing.T) {
	dom := []byte(`<html><pre id="rkb-measure">[{"id":"b1","text":"Cut it \ud83d","lines":2,"last_line_fill":0.25},` +
		`{"id":"b2","text":"&lt;tag&gt; &amp; more","lines":1,"last_line_fill":0.9}]</pre></html>`)
	bm, err := parseMeasure(dom)
	if err != nil {
		t.Fatal(err)
	}
	if len(bm) != 2 || bm[0].ID != "b1" || bm[0].Lines != 2 || bm[0].LastLineFill != 0.25 || bm[1].Text != "<tag> & more" {
		t.Fatalf("metrics = %+v", bm)
	}
	if _, err := parseMeasure([]byte(`<html></html>`)); err == nil {
		t.Error("a page without the measurement block gave no error")
	}
	if _, err := parseMeasure([]byte(`<pre id="rkb-measure">[{"id":</pre>`)); err == nil {
		t.Error("a truncated measurement gave no error")
	}
}
