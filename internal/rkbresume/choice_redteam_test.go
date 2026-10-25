package rkbresume

import (
	"strings"
	"testing"
)

// Red-team tests of the choice validator (parseChoice, checkKeywords).

// rtExperience is a valid Experience section; entries are appended to it.
func rtExperience(extra ...string) string {
	ents := []string{`{"entry":"exp-a","bullets":["a3","a1"]}`, `{"entry":"exp-b","bullets":["b1"]}`}
	ents = append(ents, extra...)
	return `{"title":"Experience","entries":[` + strings.Join(ents, ",") + `]}`
}

// A summary bullet is keyed under entry "" in the pool. An entries section that
// names entry "" therefore finds the summary bullets "available under it".
func TestRedteamEntryIDEmptyDoesNotReachTheSummaryPool(t *testing.T) {
	_, b, p := rtContBuilder(t)
	reply := replyWith(secSummary, rtExperience(`{"entry":"","bullets":["s2"]}`))
	if _, err := b.parseChoice(reply, p, testJD); err == nil {
		t.Fatalf(`an entry with id "" was accepted in an entries section and carried summary bullet s2`)
	}
}

// The same break end to end: what rkb is asked to render.
func TestRedteamEntryIDEmptyNeverReachesRkb(t *testing.T) {
	f := newFixture(t)
	f.provider.replies = []string{replyWith(secSummary, rtExperience(`{"entry":"","bullets":["s2"]}`)), validReply()}
	if _, err := f.build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, sel := range f.rkb.sels {
		if strings.Contains(sel, `"entry": ""`) {
			t.Fatalf("a selection naming entry \"\" (carrying summary bullet s2) was handed to rkb, so rkb was asked to render it")
		}
	}
}

// Every id the model may smuggle must be refused. Each case is one reply that
// differs from the valid one in exactly one place.
func TestRedteamSmuggledIDsAreRefused(t *testing.T) {
	_, b, p := rtContBuilder(t)
	kw := mustJSON(testKeywords)
	reply := func(summary, exp string) string {
		return `{"sections":[{"title":"Summary","bullets":` + summary + `},{"title":"Experience","entries":` + exp + `}],"jd_keywords":` + kw + `}`
	}
	okExp := `[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]`
	cases := map[string]string{
		"entry A's bullet under entry B":     reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-b","bullets":["b1","a8"]}]`),
		"entry bullet in summary":            reply(`["a1"]`, okExp),
		"summary bullet under an entry":      reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3","s1"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"same bullet twice across sections":  reply(`["s1"]`, `[{"entry":"exp-a","bullets":["s1"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"same bullet twice in a list":        reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3","a3"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"ineligible bullet x1":               reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"exp-c","bullets":["x1"]}]`),
		"cyrillic a in the id":               reply(`["s3"]`, `[{"entry":"exp-a","bullets":["а3"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"fullwidth digit in the id":          reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a３"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"trailing space in the id":           reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3 "]},{"entry":"exp-b","bullets":["b1"]}]`),
		"zero-width joiner in the id":        reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a‍3"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"uppercase id":                       reply(`["s3"]`, `[{"entry":"exp-a","bullets":["A3"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"entry id in another case":           reply(`["s3"]`, `[{"entry":"EXP-A","bullets":["a3"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"entry listed twice":                 reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-a","bullets":["a1"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"null bullet id":                     reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3",null]},{"entry":"exp-b","bullets":["b1"]}]`),
		"null entry":                         reply(`["s3"]`, `[{"entry":null,"bullets":["a3"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"null bullets list":                  reply(`["s3"]`, `[{"entry":"exp-a","bullets":null},{"entry":"exp-b","bullets":["b1"]}]`),
		"number as id":                       reply(`["s3"]`, `[{"entry":"exp-a","bullets":[3]},{"entry":"exp-b","bullets":["b1"]}]`),
		"edu entry (not a chosen section)":   reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]},{"entry":"exp-b","bullets":["b1"]},{"entry":"edu","bullets":["a1"]}]`),
		"work history entry exp-b left out":  reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a3"]}]`),
		"seven bullets under one entry":      reply(`["s3"]`, `[{"entry":"exp-a","bullets":["a1","a2","a3","a4","a5","a6","a7"]},{"entry":"exp-b","bullets":["b1"]}]`),
		"five summary bullets":               reply(`["s1","s2","s3","a1","a2"]`, okExp),
		"entries inside the summary section": `{"sections":[{"title":"Summary","entries":[{"entry":"exp-a","bullets":["a1"]}]},{"title":"Experience","entries":` + okExp + `}],"jd_keywords":` + kw + `}`,
	}
	for name, r := range cases {
		if _, err := b.parseChoice(r, p, testJD); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The control: the unmodified reply is accepted, so the refusals above are
	// about their one change.
	if _, err := b.parseChoice(reply(`["s3","s1"]`, okExp), p, testJD); err != nil {
		t.Fatalf("control reply refused: %v", err)
	}
}

// The reply is parsed once, strictly. Anything that is not exactly the object
// asked for is refused.
func TestRedteamReplyFramingIsStrict(t *testing.T) {
	_, b, p := rtContBuilder(t)
	good := validReply()
	cases := map[string]string{
		"two objects":               good + good,
		"object then array":         good + `[]`,
		"object then garbage":       good + ` x`,
		"leading prose":             "Here you go: " + good,
		"fence with trailing prose": "```json\n" + good + "\n```\nHope that helps",
		"unknown field":             strings.Replace(good, `"jd_keywords"`, `"note":"x","jd_keywords"`, 1),
		"huge reply":                good + strings.Repeat(" ", maxReplyBytes),
		"empty":                     "",
		"null":                      "null",
		"array":                     "[" + good + "]",
		"utf8 bom":                  string([]byte{0xef, 0xbb, 0xbf}) + good,
	}
	for name, r := range cases {
		if _, err := b.parseChoice(r, p, testJD); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, r := range map[string]string{
		"fence":       "```json\n" + good + "\n```",
		"whitespace":  "\n\t " + good + " \n",
		"bare fence":  "```\n" + good + "\n```",
		"json prefix": "```json" + good + "```",
	} {
		if _, err := b.parseChoice(r, p, testJD); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// Go's decoder keeps the last of two equal keys and matches keys ignoring case.
// loadBase refuses duplicate keys in the base selection (kbsync.ParseSelection);
// the model's reply gets no such check, so a reply can carry two different
// "sections" and the validator only ever sees one of them.
func TestRedteamReplyWithDuplicateKeysIsRefused(t *testing.T) {
	_, b, p := rtContBuilder(t)
	good := validReply()
	bad := `{"sections":[{"title":"Summary","bullets":["nonsense"]}],` + strings.TrimPrefix(good, "{")
	if _, err := b.parseChoice(bad, p, testJD); err == nil {
		t.Errorf("a reply with two sections keys was accepted (the second silently replaced the first)")
	}
	mixed := strings.Replace(good, `"jd_keywords"`, `"JD_KEYWORDS":[],"jd_keywords"`, 1)
	if _, err := b.parseChoice(mixed, p, testJD); err == nil {
		t.Errorf("a reply with jd_keywords twice (different case) was accepted")
	}
	caps := strings.Replace(good, `"sections"`, `"Sections"`, 1)
	if _, err := b.parseChoice(caps, p, testJD); err == nil {
		t.Errorf(`a reply spelling "Sections" in another case was accepted; the shape is exact`)
	}
}

// keywords are metadata for rkb lint (a "JD keywords not on the page" warning);
// rkb does not render them (rkb render.go, ats.go, docx.go, latex.go never read
// Meta.JDKeywords). They must still occur in the posting as written.
func TestRedteamKeywordsOccurInThePostingAsWritten(t *testing.T) {
	jd := "We use k8s and the Kelvin scale with tracing, latency, batching, observability and Go."
	base := []string{"Kelvin", "scale", "tracing", "latency", "batching", "observability", "Go"}
	// U+212A KELVIN SIGN lowercases to "k", so it matches "k8s" only through
	// Unicode folding; the keyword the model returned is not in the posting.
	kelvin := string(rune(0x212a)) + "8s"
	out, err := checkKeywords(append(append([]string{}, base...), kelvin), jd)
	if err == nil {
		t.Errorf("keyword %q was accepted as occurring in the posting (it only matches after Unicode lowercasing): %q", kelvin, out)
	}
}

// A keyword is any substring: one letter satisfies "words or short phrases that
// occur verbatim". Eight single letters are a valid keyword list.
func TestRedteamKeywordsAreWordsNotLetters(t *testing.T) {
	out, err := checkKeywords([]string{"a", "e", "i", "o", "u", "s", "t", "n"}, testJD)
	if err == nil {
		t.Errorf("eight single letters were accepted as jd_keywords: %v", out)
	}
}

// Markup in a keyword is data. It may be accepted (it occurs in the posting),
// but it must never reach rkb unescaped: the selection is JSON, and rkb only
// compares keywords with the page text. This pins the escaping boundary.
func TestRedteamKeywordMarkupStaysInertInTheSelection(t *testing.T) {
	f := newFixture(t)
	jd := testJD + ` Also <img src=x onerror=alert(1)> and "quoted" and </script> and {{clmUnv}} and **bold** and [x](http://evil.example).`
	kws := []string{"Kubernetes", "Go", "Rust", "tracing", "latency", "batching", "observability", `<img src=x onerror=alert(1)>`, `</script>`, `{{clmUnv}}`, `**bold**`}
	f.provider.replies = []string{`{"sections":[{"title":"Summary","bullets":["s3","s1"]},{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3","a1"]},{"entry":"exp-b","bullets":["b1"]}]}],"jd_keywords":` + mustJSON(kws) + `}`}
	res, err := Build(t.Context(), f.cfg, Request{Company: "Example AI", Title: "Inference Engineer", Description: jd})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Source != SourceTailored {
		t.Skipf("scenario did not reach rkb as tailored: %s", res.Reason)
	}
	sel := f.rkb.sels[len(f.rkb.sels)-1]
	// The keyword text is only ever inside meta.jd_keywords, as JSON strings,
	// and a claim token inside a keyword must not be resolved by this package.
	if strings.Count(sel, "onerror=alert(1)") != 1 {
		t.Errorf("the markup keyword appears outside meta.jd_keywords or more than once:\n%s", sel)
	}
	if strings.Contains(sel, `"entry": "`+"{{clmUnv}}") {
		t.Errorf("a keyword became an entry")
	}
}
