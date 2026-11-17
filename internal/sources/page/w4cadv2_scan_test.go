package page

import "testing"

// In HTML (not SVG) a "<script/>" start tag is a script element that runs to
// "</script>"; with none, a browser reads the rest of the document as script.
// The scan treats it as empty and lists the links after it, and reports the
// document complete.
func TestW4cadv2SelfClosedScriptInHTMLSwallowsTheRest(t *testing.T) {
	doc := `<html><body><script src="/a.js"/><a href="/jobs/1">J</a></body></html>`
	sc := scanHTML(doc)
	if len(sc.links) != 0 && sc.truncated == "" {
		t.Fatalf("browser sees no link and an unclosed script; scan found %d links, truncated %q", len(sc.links), sc.truncated)
	}
}

// A comment whose only "--!" or "---!>" never closes stays truncated.
func TestW4cadv2CommentBangNeverClosedIsTruncated(t *testing.T) {
	for _, doc := range []string{`<a href="/jobs/1">J</a><!-- a --! b`, `<a href="/jobs/1">J</a><!---!>`} {
		if sc := scanHTML(doc); sc.truncated == "" {
			t.Errorf("%q read as complete", doc)
		}
	}
}
