package htmltext

import (
	"strings"
	"testing"
)

// benchPage is a careers-page-like document: navigation, a script, a style,
// a comment and n paragraphs of mixed-case markup.
func benchPage(n int) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><HTML><Head><Title>Careers</Title><STYLE>body{color:red}</STYLE>`)
	b.WriteString(`<script>var a = "<p>not text</p>";</SCRIPT></head><BODY><nav><UL><li>Home<li>Jobs</ul></nav>`)
	for i := range n {
		if i%50 == 0 {
			b.WriteString("<!-- section -->")
		}
		b.WriteString(`<DIV class="job"><H2>Inference Engineer</H2><p>We build LLM serving in Go &amp; Rust, ` +
			`with <b>CUDA</b> kernels and <A HREF="/jobs/1">observability</A>.</p><TD>Remote</td></div>`)
	}
	b.WriteString("</BODY></HTML>")
	return b.String()
}

// A posting page of about 200 KiB, cut to a description-sized result.
func BenchmarkText(b *testing.B) {
	doc := benchPage(1000)
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		_ = Text(doc, 64<<10)
	}
}

// The case the card names: a 16 MiB document from which at most 256 KiB is
// kept. The reading stops at the budget, so the cost should track the
// budget, not the document.
func BenchmarkTextLargeDocument(b *testing.B) {
	doc := benchPage(16 << 20 / 200)
	b.ReportAllocs()
	for b.Loop() {
		_ = Text(doc, 256<<10)
	}
}

func BenchmarkPlain(b *testing.B) {
	line := "  Build   LLM serving\tin Go and Rust;\x01 own latency, cost and reliability.  \n\n\n"
	s := strings.Repeat(line, 2000)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for b.Loop() {
		_ = Plain(s, 64<<10)
	}
}
