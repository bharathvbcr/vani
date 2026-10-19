package htmltext

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "hello  world", "hello world"},
		{"paragraphs", "<p>One</p><p>Two</p>", "One\nTwo"},
		{"source newline is a space", "<p>a\nb\r\nc</p>", "a b c"},
		{"br and li", "a<br>b<br/>c<ul><li>x</li><li>y</li></ul>", "a\nb\nc\nx\ny"},
		{"nested lists", "<ul><li>a<ul><li>a1</li><li>a2</li></ul></li><li>b</li></ul>", "a\na1\na2\nb"},
		{"inline tags keep words joined", "he<b>llo</b> <i>wor</i>ld", "hello world"},
		{"cells", "<tr><td>a</td><td>b</td></tr>", "a b"},
		{"script dropped", "a<script>alert('x<b>')</script>b", "ab"},
		{"style dropped, upper case", "a<STYLE>p{color:red}</STYLE>b", "ab"},
		{"script close with space", "a<script>x</script >b", "ab"},
		{"comment dropped", "a<!-- <p>hidden</p> -->b", "ab"},
		{"doctype and pi dropped", "<!DOCTYPE html><?xml v?>hi", "hi"},
		{"entities", "Tom &amp; Jerry &lt;3 &#39;q&#39; &copy; 2026", "Tom & Jerry <3 'q' © 2026"},
		{"escaped markup is not re-parsed", "&lt;script&gt;alert(1)&lt;/script&gt;", "<script>alert(1)</script>"},
		{"nbsp collapses", "a&nbsp;&nbsp;b", "a b"},
		{"attributes with newline", "<a\nhref=\"x\"\nclass='y'>link</a>", "link"},
		{"literal less-than", "1 < 2 and 3<4", "1 < 2 and 3<4"},
		{"unclosed tag stays literal", "text <div class=", "text <div class="},
		{"unclosed element", "<p>open <b>bold <i>italic", "open bold italic"},
		{"unclosed script drops rest", "keep<script>drop me", "keep"},
		{"unclosed comment stays literal text", "keep<!-- drop me", "keep<!-- drop me"},
		{"comment containing gt", "a<!-- x > y -->b", "ab"},
		{"stray close tags", "</div></p></b>x", "x"},
		{"control chars removed", "a\x00b\x07c\x1b[0m", "abc[0m"},
		{"invalid utf8 removed", "a\xffb\xc3", "ab"},
		{"adjacent blocks give one break", "<p>a</p><p></p><p></p><p></p><p>b</p>", "a\nb"},
		{"leading block gives no break", "<div><p>a</p></div>", "a"},
		{"heading", "<h1>T</h1>body", "T\nbody"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Text(c.in, 1<<16); got != c.want {
				t.Fatalf("Text(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestTextNeverEmitsMarkup(t *testing.T) {
	in := `<p onclick="x()">hi</p><script>bad()</script><img src=x onerror=alert(1)><a href="javascript:x">l</a>`
	got := Text(in, 1<<16)
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("markup leaked: %q", got)
	}
	if got != "hi\nl" {
		t.Fatalf("got %q", got)
	}
}

func TestTextCap(t *testing.T) {
	got := Text(strings.Repeat("<p>word</p>", 100000), 100)
	if len(got) > 100 || len(got) < 90 {
		t.Fatalf("len %d, want about 100", len(got))
	}
	if Text("abc", 0) != "" || Text("abc", -1) != "" {
		t.Fatal("non-positive max must yield empty")
	}
}

func TestCapKeepsRuneBoundary(t *testing.T) {
	got := Text(strings.Repeat("é", 1000), 101) // 2 bytes each
	if !utf8.ValidString(got) {
		t.Fatalf("cut inside a rune: %q", got[len(got)-4:])
	}
	if len(got) > 101 {
		t.Fatalf("len %d > 101", len(got))
	}
}

func TestTextHugeAndHostileInputIsBounded(t *testing.T) {
	inputs := map[string]string{
		"many open angle brackets":  strings.Repeat("<a ", 2_000_000),
		"many literal lt":           strings.Repeat("< ", 2_000_000),
		"many unclosed scripts":     strings.Repeat("<script>", 500_000),
		"many comments openers":     strings.Repeat("<!--", 500_000),
		"deep nesting":              strings.Repeat("<div>", 1_000_000) + "end" + strings.Repeat("</div>", 1_000_000),
		"huge entity soup":          strings.Repeat("&amp;&lt;&#x41;&bogus;", 500_000),
		"whitespace only":           strings.Repeat(" \n\t", 3_000_000),
		"plain megabytes":           strings.Repeat("word ", 4_000_000),
		"tags then text":            strings.Repeat("<b>", 3_000_000) + "tail",
		"tag with huge attr":        "<a href=\"" + strings.Repeat("x", 10_000_000) + "\">t</a>",
		"nul and invalid utf8 mass": strings.Repeat("\x00\xff", 2_000_000),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			got := Text(in, 1<<16)
			if len(got) > 1<<16 {
				t.Fatalf("len %d exceeds cap", len(got))
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("took %s: input is not processed in linear time", d)
			}
		})
	}
	if got := Text(inputs["deep nesting"], 1<<16); got != "end" {
		t.Fatalf("deep nesting: got %q", got)
	}
}

func TestPlain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  a   b \n\n\n\n c\r\n d  ", "a b\n\nc\nd"},
		{"\n\n\nlead", "lead"},
		{"x\x00y", "xy"},
		{"<b>kept</b>", "<b>kept</b>"}, // plain text is not parsed as markup
	}
	for _, c := range cases {
		if got := Plain(c.in, 1<<16); got != c.want {
			t.Errorf("Plain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := Plain(strings.Repeat("line\n", 100000), 50); len(got) > 50 {
		t.Errorf("Plain cap: len %d", len(got))
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("hello world", 5); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := Truncate("hello world", 6); got != "hello" {
		t.Errorf("trailing space not trimmed: %q", got)
	}
	if got := Truncate("short", 100); got != "short" {
		t.Errorf("got %q", got)
	}
}
