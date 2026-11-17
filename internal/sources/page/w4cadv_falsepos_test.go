package page

import "testing"

// Realistic, healthy markup that a browser reads to the end must not be reported
// as cut off: ErrTruncatedDocument fails the whole board.
func TestW4cadvHealthyMarkupIsNotTruncated(t *testing.T) {
	for name, doc := range map[string]string{
		"unclosed p li td":         `<ul><li>a<li>b</ul><table><tr><td>x<td>y</table><p>one<p>two<a href="/jobs/1">J</a>`,
		"script with close-ish":    `<script>var s = "<\/div> </ not-a-tag"; if (a < b) {}</script><a href="/jobs/1">J</a>`,
		"comment with dashes":      `<!-- a -- b --- c --><a href="/jobs/1">J</a>`,
		"cdata":                    `<div><![CDATA[ x < y ]]></div><a href="/jobs/1">J</a>`,
		"svg title":                `<svg><title>Logo</title><path d="M0 0"/></svg><a href="/jobs/1">J</a>`,
		"conditional comment":      `<!--[if lt IE 9]><script src="x.js"></script><![endif]--><a href="/jobs/1">J</a>`,
		"downlevel revealed":       `<![if !IE]><a href="/jobs/1">J</a><![endif]>`,
		"empty comment html5":      `<!----><a href="/jobs/1">J</a>`,
		"abrupt empty comment":     `<!--><a href="/jobs/1">J</a>`,
		"abrupt empty comment 2":   `<!---><a href="/jobs/1">J</a>`,
		"bang close comment":       `<!-- x --!><a href="/jobs/1">J</a>`,
		"email in angle brackets":  `<p>Contact <jobs@example.com> today</p><a href="/jobs/1">J</a>`,
		"stray lt in text":         `<p>salary <$100k and a<b 'x</p><a href="/jobs/1">J</a>`,
		"unquoted attr apostrophe": `<a href=/jobs/1 title=It's>J</a>`,
		"self closing title":       `<svg><title/></svg><a href="/jobs/1">J</a>`,
	} {
		if sc := scanHTML(doc); sc.truncated != "" {
			t.Errorf("%s: healthy document reported truncated: %s (links %d)", name, sc.truncated, len(sc.links))
		}
	}
}
