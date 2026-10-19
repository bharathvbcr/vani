package page

import (
	"fmt"
	"strings"
	"testing"
)

// benchDOM is a DOM dump the size of a large careers listing: n anchors in
// mixed-case markup, with scripts, styles, JSON-LD and comments around them.
func benchDOM(n int) string {
	var b strings.Builder
	b.WriteString(`<HTML><head><TITLE>Open roles</TITLE><base HREF="https://acme.example/"><STYLE>a{}</STYLE>`)
	b.WriteString(`<script type="application/ld+json">{"@type":"JobPosting","title":"x"}</SCRIPT></head><Body>`)
	for i := range n {
		if i%100 == 0 {
			b.WriteString(`<!-- page break --><script>var s = "<a href='/no'>no</a>";</script>`)
		}
		fmt.Fprintf(&b, `<DIV CLASS="row"><A Href="/careers/%d" DATA-ID="%d">Senior <b>Engineer</b> %d</A><span>Remote</span></div>`, i, i, i)
	}
	b.WriteString("</Body></HTML>")
	return b.String()
}

func BenchmarkScanHTML(b *testing.B) {
	doc := benchDOM(5000)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if sc := scanHTML(doc); len(sc.links) != 5000 {
			b.Fatalf("%d links", len(sc.links))
		}
	}
}
