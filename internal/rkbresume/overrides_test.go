package rkbresume

import (
	"encoding/json"
	"strings"
	"testing"
)

// editWith is an owner's edit: summary s3, then experience with the entries
// given as JSON fragments, and the test keywords.
func editWith(exp ...string) string {
	return `{"sections":[{"title":"Summary","bullets":["s3"]},{"title":"Experience","entries":[` + strings.Join(exp, ",") + `]}],"jd_keywords":` + mustJSON(testKeywords) + `}`
}

// selEntry finds the entry id in a selection JSON, as rkb reads it.
func selEntry(t *testing.T, sel []byte, id string) map[string]any {
	t.Helper()
	var s struct {
		Sections []struct {
			Entries []map[string]any `json:"entries"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(sel, &s); err != nil {
		t.Fatal(err)
	}
	for _, sec := range s.Sections {
		for _, e := range sec.Entries {
			if e["entry"] == id {
				return e
			}
		}
	}
	t.Fatalf("entry %q is not in the selection:\n%s", id, sel)
	return nil
}

func ownerSel(t *testing.T, b *builder, p pool, text string) (choice, []byte) {
	t.Helper()
	ch, err := b.ownerChoice([]byte(text), p, testJD)
	if err != nil {
		t.Fatalf("ownerChoice: %v", err)
	}
	sel, err := b.base.selectionJSON(b.cfg.Bank, ch)
	if err != nil {
		t.Fatalf("selectionJSON: %v", err)
	}
	return ch, sel
}

// An override reaches the selection rkb is handed, over the base entry's own
// field; the base's other fields stay.
func TestAnOverrideReachesTheSelectionOverTheBaseEntry(t *testing.T) {
	_, b, p := rtContBuilder(t)
	_, sel := ownerSel(t, b, p, editWith(
		`{"entry":"exp-a","bullets":["a3"],"title":"Staff Engineer","stack":"Go, Rust","lead":"-","dates":"2022 – 2024"}`,
		`{"entry":"exp-b","bullets":["b1"]}`))
	e := selEntry(t, sel, "exp-a")
	want := map[string]string{"title": "Staff Engineer", "stack": "Go, Rust", "lead": "-", "dates": "2022 – 2024"}
	for k, v := range want {
		if e[k] != v {
			t.Errorf("%s = %v, want %q (entry %v)", k, e[k], v, e)
		}
	}
	if _, ok := e["org"]; ok {
		t.Errorf("org was written though nothing overrides it: %v", e)
	}
}

// An entry borrowed from another selection has no base entry to copy from: its
// override is all it carries, and rkb fills the rest from the bank.
func TestABorrowedEntrysOverrideIsWritten(t *testing.T) {
	_, b, p := rtContBuilder(t)
	_, sel := ownerSel(t, b, p, editWith(
		`{"entry":"exp-a","bullets":["a3"]}`, `{"entry":"exp-b","bullets":["b1"]}`,
		`{"entry":"exp-c","bullets":["c1"],"title":"Platform Intern","org":"Co Labs"}`))
	e := selEntry(t, sel, "exp-c")
	if e["title"] != "Platform Intern" || e["org"] != "Co Labs" {
		t.Errorf("overrides missing from the borrowed entry: %v", e)
	}
	for _, k := range []string{"stack", "lead", "dates"} {
		if _, ok := e[k]; ok {
			t.Errorf("%s written on the borrowed entry with no override: %v", k, e)
		}
	}
}

// With no override the base entry's own fields go through untouched.
func TestNoOverrideLeavesTheBaseFieldUntouched(t *testing.T) {
	_, b, p := rtContBuilder(t)
	_, sel := ownerSel(t, b, p, editWith(
		`{"entry":"exp-a","bullets":["a3"],"title":"","stack":""}`, `{"entry":"exp-b","bullets":["b1"]}`))
	e := selEntry(t, sel, "exp-a")
	if e["title"] != "Lead Engineer" || e["lead"] != "A lead line." {
		t.Errorf("base fields changed: %v", e)
	}
	if _, ok := e["stack"]; ok {
		t.Errorf("an empty override was written: %v", e)
	}
}

// without() (dropping unverified bullets) and carryHeadings (adding the base's
// bare entries) keep the overrides of the entries that stay.
func TestOverridesSurviveWithoutAndCarryHeadings(t *testing.T) {
	f := newFixture(t)
	editJSON(t, f.base, func(m map[string]any) { // exp-b is listed bare: carried in as a heading
		exp := m["sections"].([]any)[1].(map[string]any)
		exp["entries"].([]any)[1] = map[string]any{"entry": "exp-b", "bullets": []string{}}
	})
	b := &builder{cfg: f.cfg, dir: t.TempDir()}
	if err := b.load(t.Context()); err != nil {
		t.Fatal(err)
	}
	p := b.buildPool(func(string) bool { return true })
	ch, err := b.ownerChoice([]byte(editWith(`{"entry":"exp-a","bullets":["a3","a1"],"stack":"Go"}`)), p, testJD)
	if err != nil {
		t.Fatalf("ownerChoice: %v", err)
	}
	exp := ch.secs[b.base.expIdx].entries
	if len(exp) != 2 || exp[0].id != "exp-a" || exp[0].over.Stack != "Go" || exp[1].id != "exp-b" || !exp[1].keepEmpty {
		t.Fatalf("experience = %+v, want exp-a with its override and exp-b carried as a heading", exp)
	}
	out, _ := ch.without(map[string]bool{"a3": true}, func(string) bool { return false })
	exp = out.secs[b.base.expIdx].entries
	if len(exp) != 2 || exp[0].over.Stack != "Go" || len(exp[0].bullets) != 1 {
		t.Errorf("without lost the override: %+v", exp)
	}
	// And the heading carried in can itself be overridden: it is on the page.
	ch, err = b.ownerChoice([]byte(editWith(`{"entry":"exp-a","bullets":["a3"]}`)), p, testJD)
	if err != nil {
		t.Fatal(err)
	}
	if err := attachOverrides(&ch, map[string]entryOverride{"exp-b": {Dates: "2021"}}); err != nil {
		t.Errorf("an override of a carried heading was refused: %v", err)
	}
}

func TestCheckOverridesRefusesWhatRkbWouldRender(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := map[string]struct {
		o    entryOverride
		want string // "" = accepted
	}{
		"plain fields":             {entryOverride{Title: "Staff Engineer", Org: "Acme, Inc.", Stack: "Go / Rust", Lead: "Led the team.", Dates: "Jan 2020 – Present"}, ""},
		"nothing overridden":       {entryOverride{}, ""},
		"lead suppress":            {entryOverride{Lead: "-"}, ""},
		"unicode letters":          {entryOverride{Title: "Ingénieur Logiciel", Dates: "Janvier 2020"}, ""},
		"title at the limit":       {entryOverride{Title: long(120)}, ""},
		"title over the limit":     {entryOverride{Title: long(121)}, "title is over 120"},
		"org over the limit":       {entryOverride{Org: long(121)}, "org is over 120"},
		"stack over the limit":     {entryOverride{Stack: long(121)}, "stack is over 120"},
		"lead at the limit":        {entryOverride{Lead: long(300)}, ""},
		"lead over the limit":      {entryOverride{Lead: long(301)}, "lead is over 300"},
		"dates at the limit":       {entryOverride{Dates: long(40)}, ""},
		"dates over the limit":     {entryOverride{Dates: long(41)}, "dates is over 40"},
		"runes not bytes":          {entryOverride{Title: strings.Repeat("é", 120)}, ""},
		"claim token open":         {entryOverride{Title: "Cut {{clmLat}}"}, `"{{"`},
		"claim token close":        {entryOverride{Lead: "x }} y"}, `"}}"`},
		"markdown link":            {entryOverride{Lead: "see [here](http://x.invalid)"}, `"]("`},
		"emphasis star":            {entryOverride{Stack: "**Go**"}, `"*"`},
		"single star":              {entryOverride{Org: "A*B"}, `"*"`},
		"backtick":                 {entryOverride{Title: "`code`"}, "`"},
		"less than":                {entryOverride{Title: "a<b"}, `"<"`},
		"greater than":             {entryOverride{Lead: "a>b"}, `">"`},
		"html tag":                 {entryOverride{Lead: "<b>x</b>"}, `"<"`},
		"newline":                  {entryOverride{Lead: "one\ntwo"}, "one printable line"},
		"carriage return":          {entryOverride{Title: "a\rb"}, "one printable line"},
		"tab":                      {entryOverride{Stack: "Go\tRust"}, "one printable line"},
		"NUL":                      {entryOverride{Org: "a\x00b"}, "one printable line"},
		"DEL":                      {entryOverride{Org: "a\x7fb"}, "one printable line"},
		"right-to-left override":   {entryOverride{Title: "Engineer‮"}, "one printable line"},
		"left-to-right isolate":    {entryOverride{Lead: "x⁦y"}, "one printable line"},
		"zero width space":         {entryOverride{Title: "a​b"}, "one printable line"},
		"line separator":           {entryOverride{Lead: "a b"}, "one printable line"},
		"no-break space":           {entryOverride{Title: "a b"}, "one printable line"},
		"invalid UTF-8":            {entryOverride{Title: "a\xffb"}, "not valid text"},
		"whitespace only":          {entryOverride{Title: "   "}, "only whitespace"},
		"newline only":             {entryOverride{Lead: "\n"}, "only whitespace"},
		"padded":                   {entryOverride{Org: " Acme"}, "space around"},
		"suppress with padding":    {entryOverride{Lead: "-\n"}, "space around"},
		"dates with a colon":       {entryOverride{Dates: "2020: 2021"}, "dates may hold only"},
		"dates with parentheses":   {entryOverride{Dates: "2020 (Jan)"}, "dates may hold only"},
		"dates with a semicolon":   {entryOverride{Dates: "2020;2021"}, "dates may hold only"},
		"dates with an underscore": {entryOverride{Dates: "2020_2021"}, "dates may hold only"},
		"dates with a minus sign":  {entryOverride{Dates: "2020 − 2021"}, "dates may hold only"},
		"dates with every allowed": {entryOverride{Dates: "Sep 2019 – Dec 2020, 5/6."}, ""},
	}
	for name, c := range cases {
		err := checkOverrides(map[string]entryOverride{"exp-a": c.o})
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want one containing %q", name, err, c.want)
		}
	}
}

// A refusal names the field and the entry, never the text the owner typed.
func TestOverrideRefusalsDoNotEchoTheText(t *testing.T) {
	err := checkOverrides(map[string]entryOverride{"exp-a": {Lead: "reach me at secret@example.invalid **now**"}})
	if err == nil || strings.Contains(err.Error(), "secret@example.invalid") || !strings.Contains(err.Error(), "lead") || !strings.Contains(err.Error(), "exp-a") {
		t.Errorf("err = %v", err)
	}
}

func TestDecodeEditReadsTheFiveOverrideKeys(t *testing.T) {
	r, ov, err := decodeEdit([]byte(editWith(
		`{"entry":"exp-a","bullets":["a3"],"title":"T","stack":"S","lead":"L","org":"O","dates":"2020"}`,
		`{"entry":"exp-b","bullets":["b1"]}`,
		`{"entry":"exp-c","bullets":["c1"],"title":"","lead":""}`)))
	if err != nil {
		t.Fatalf("decodeEdit: %v", err)
	}
	if len(r.Sections) != 2 || len(r.Sections[1].Entries) != 3 {
		t.Errorf("reply = %+v", r)
	}
	if want := (entryOverride{Title: "T", Stack: "S", Lead: "L", Org: "O", Dates: "2020"}); len(ov) != 1 || ov["exp-a"] != want {
		t.Errorf("overrides = %+v, want only exp-a: %+v", ov, want)
	}
}

func TestDecodeEditIsStrict(t *testing.T) {
	ok := editWith(`{"entry":"exp-a","bullets":["a3"],"title":"T"}`)
	pad := strings.Repeat("a", maxEditBytes)
	cases := map[string]string{
		"over the byte bound":   editWith(`{"entry":"exp-a","bullets":["a3"],"title":"` + pad + `"}`),
		"text after the object": ok + ` {}`,
		"two objects":           ok + ok,
		"not an object":         `[]`,
		"empty":                 ``,
		"repeated top key":      `{"sections":[{"title":"Summary","bullets":["s3"]}],"sections":[],"jd_keywords":[]}`,
		"repeated entry key":    editWith(`{"entry":"exp-a","bullets":["a3"],"title":"A","title":"B"}`),
		"repeated override key": editWith(`{"entry":"exp-a","bullets":["a3"],"lead":"A","lead":"B"}`),
		"entry id with a space": editWith(`{"entry":"exp a","bullets":["a3"]}`),
		"bullet id with a path": editWith(`{"entry":"exp-a","bullets":["../a3"]}`),
		"unknown top key":       `{"sections":[{"title":"Summary","bullets":["s3"]}],"jd_keywords":[],"header":{}}`,
		"unknown section key":   `{"sections":[{"title":"Summary","bullets":["s3"],"lead":"x"}],"jd_keywords":[]}`,
		"unknown entry key":     editWith(`{"entry":"exp-a","bullets":["a3"],"bank":"x.json"}`),
		"capitalised override":  editWith(`{"entry":"exp-a","bullets":["a3"],"Title":"T"}`),
		"override not a string": editWith(`{"entry":"exp-a","bullets":["a3"],"title":5}`),
		"override a list":       editWith(`{"entry":"exp-a","bullets":["a3"],"lead":["x"]}`),
		"entry listed twice":    editWith(`{"entry":"exp-a","bullets":["a3"],"title":"A"}`, `{"entry":"exp-a","bullets":["a1"],"title":"B"}`),
	}
	if _, _, err := decodeEdit([]byte(ok)); err != nil {
		t.Fatalf("the control edit was refused: %v", err)
	}
	for name, text := range cases {
		if _, _, err := decodeEdit([]byte(text)); err == nil {
			t.Errorf("%s: decodeEdit accepted it", name)
		}
	}
}

// The model's reply is exactly as strict as before: an override key is not one
// it may use, at any spelling.
func TestAModelReplyWithAnOverrideKeyIsStillRefused(t *testing.T) {
	for _, k := range []string{"title", "stack", "lead", "org", "dates"} {
		text := replyWith(secSummary, `{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a3"],"`+k+`":"x"}]}`)
		if _, err := decodeReply(text); err == nil {
			t.Errorf("decodeReply accepted an entry with %q", k)
		}
		_, b, p := rtContBuilder(t)
		if _, err := b.parseChoice(text, p, testJD); err == nil {
			t.Errorf("parseChoice accepted an entry with %q", k)
		}
	}
	if _, err := decodeReply(replyWith(secSummary, secExperience)); err != nil {
		t.Errorf("the control reply was refused: %v", err)
	}
}

// The owner's choice with overrides is the page: it round-trips into the
// selection, in the owner's order, and the model's steering does not bind it.
func TestOwnerChoiceRoundTripsIntoTheSelection(t *testing.T) {
	_, b, p := rtContBuilder(t)
	ch, sel := ownerSel(t, b, p, editWith(
		`{"entry":"exp-b","bullets":["b1"],"org":"Lab Two"}`,
		`{"entry":"exp-a","bullets":["a3","a1"],"stack":"Go","lead":"-"}`))
	exp := ch.secs[b.base.expIdx].entries
	if len(exp) != 2 || exp[0].id != "exp-b" || exp[0].over.Org != "Lab Two" || exp[1].id != "exp-a" || exp[1].over.Lead != "-" {
		t.Fatalf("experience = %+v", exp)
	}
	a, bb := selEntry(t, sel, "exp-a"), selEntry(t, sel, "exp-b")
	if a["stack"] != "Go" || a["lead"] != "-" || a["title"] != "Lead Engineer" || bb["org"] != "Lab Two" {
		t.Errorf("selection entries: %v %v", a, bb)
	}
	if got := selBullets(t, string(sel)); !has(got, "a3") || !has(got, "b1") {
		t.Errorf("bullets = %v", got)
	}
}

func TestOwnerChoiceIsNotHeldToTheModelsSteering(t *testing.T) {
	_, b, p := rtContBuilder(t)
	text := `{"sections":[{"title":"Summary","bullets":["s3"]},{"title":"Experience","entries":[{"entry":"exp-a","bullets":["a1","a2","a3","a4","a5","a6","a7"],"stack":"Go"}]}],"jd_keywords":[]}`
	if _, err := b.parseChoice(text, p, testJD); err == nil {
		t.Fatal("the model's reply was accepted: the case does not exercise steering")
	}
	ch, err := b.ownerChoice([]byte(text), p, testJD) // a job left out, seven bullets, no keywords
	if err != nil {
		t.Fatalf("ownerChoice: %v", err)
	}
	if got := ch.secs[b.base.expIdx].entries; len(got) != 1 || got[0].over.Stack != "Go" || len(got[0].bullets) != 7 {
		t.Errorf("experience = %+v", got)
	}
}

// Page safety still binds the owner, and a bad override refuses the whole edit.
func TestOwnerChoiceKeepsPageSafetyAndOverrideRules(t *testing.T) {
	_, b, p := rtContBuilder(t)
	cases := map[string]struct{ text, want string }{
		"ineligible bullet":   {editWith(`{"entry":"exp-c","bullets":["x1"]}`), "not available"},
		"bullet twice":        {editWith(`{"entry":"exp-a","bullets":["a3","a3"]}`), "used twice"},
		"markdown in a lead":  {editWith(`{"entry":"exp-a","bullets":["a3"],"lead":"**big**"}`), `"*"`},
		"bidi in a title":     {editWith(`{"entry":"exp-a","bullets":["a3"],"title":"x‮y"}`), "one printable line"},
		"whitespace only org": {editWith(`{"entry":"exp-a","bullets":["a3"],"org":"  "}`), "only whitespace"},
		"keyword not in jd":   {`{"sections":[{"title":"Summary","bullets":["s3"]}],"jd_keywords":["Fortran"]}`, `keyword "Fortran"`},
	}
	for name, c := range cases {
		ch, err := b.ownerChoice([]byte(c.text), p, testJD)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, c.want)
		}
		if len(ch.secs) != 0 {
			t.Errorf("%s: a choice came back with the refusal", name)
		}
	}
}

// An override for an entry that is not on the page is refused, and nothing is
// attached.
func TestAnOverrideOnAMissingEntryIsRefused(t *testing.T) {
	_, b, p := rtContBuilder(t)
	ch, err := b.checkSafe(mustReply(t, editWith(`{"entry":"exp-a","bullets":["a3"]}`)), p, testJD)
	if err != nil {
		t.Fatal(err)
	}
	for name, ov := range map[string]map[string]entryOverride{
		"an entry left off the page": {"exp-c": {Title: "X"}},
		"an entry the bank lacks":    {"nope": {Title: "X"}},
		"the summary's key":          {"": {Title: "X"}},
		"one good and one missing":   {"exp-a": {Title: "Y"}, "exp-c": {Title: "X"}},
	} {
		err := attachOverrides(&ch, ov)
		if err == nil || !strings.Contains(err.Error(), "not on the page") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Through the owner's path an overridden entry is on the page only by being
	// named with bullets that pass checkSafe.
	if _, err := b.ownerChoice([]byte(editWith(`{"entry":"exp-c","bullets":["x1"],"title":"X"}`)), p, testJD); err == nil {
		t.Error("ownerChoice accepted an ineligible bullet under an overridden entry")
	}
}
