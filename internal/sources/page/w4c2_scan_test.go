package page

import "testing"

// The comment and self-closing forms that read as healthy must not loosen
// real truncation detection: a comment, script, title or style that is
// actually cut off is still reported.
func TestW4c2TruncationStillDetected(t *testing.T) {
	for name, doc := range map[string]string{
		"comment cut":             `<a href="/jobs/1">J</a><!-- x --`,
		"comment cut after bang":  `<a href="/jobs/1">J</a><!-- x --!`,
		"comment never closes":    `<a href="/jobs/1">J</a><!-- x - y -> z`,
		"bare open comment":       `<a href="/jobs/1">J</a><!--`,
		"script cut":              `<script>var x = 1;`,
		"unquoted slash script":   `<script src=/a/>var x = 1;`,
		"slash inside attr value": `<script data-x="a/">var x = 1;`,
		"title cut":               `<title>Jobs`,
		"style cut":               `<style>a{}`,
		"self-closed then cut":    `<svg><title/></svg><script>var x`,
		"script/ in html":         `<script src="x.js" /><a href="/jobs/1">J</a>`,
		"style/ in html":          `<style/><a href="/jobs/1">J</a>`,
		"title/ in html":          `<title/><a href="/jobs/1">J</a>`,
		"script/ after svg ends":  `<svg></svg><script/><a href="/jobs/1">J</a>`,
		"script/ after math ends": `<math><mi>x</mi></math><script/><a href="/jobs/1">J</a>`,
	} {
		if sc := scanHTML(doc); sc.truncated == "" {
			t.Errorf("%s: truncated document read as complete", name)
		}
	}
}

// A self-closed tag and an abrupt or bang-closed comment do not swallow the
// rest of the document: the links after them are still found.
func TestW4c2HealthyFormsKeepTheLinksAfterThem(t *testing.T) {
	for name, doc := range map[string]string{
		"self closed script in svg": `<svg><script src="x.js" /></svg><a href="/jobs/1">J</a>`,
		"self closed style in svg":  `<svg><style/></svg><a href="/jobs/1">J</a>`,
		"self closed title in math": `<math><title/></math><a href="/jobs/1">J</a>`,
		"abrupt comment":            `<!--><a href="/jobs/1">J</a>`,
		"abrupt comment 2":          `<!---><a href="/jobs/1">J</a>`,
		"bang close":                `<!-- x --!><a href="/jobs/1">J</a>`,
		"dash dash bang mid":        `<!-- a --!> b --><a href="/jobs/1">J</a>`,
	} {
		sc := scanHTML(doc)
		if sc.truncated != "" || len(sc.links) != 1 {
			t.Errorf("%s: truncated %q, %d links, want none and 1", name, sc.truncated, len(sc.links))
		}
	}
}
