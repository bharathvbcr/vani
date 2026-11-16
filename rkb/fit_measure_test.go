package main

import "testing"

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
