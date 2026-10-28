package ashby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// formFromFixture decodes a saved jobPosting reply the way the source does and
// parses its form.
func formFromFixture(t *testing.T, name string) *posting.ApplyForm {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var p postingPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if p.Data.JobPosting == nil {
		t.Fatalf("%s has no jobPosting", name)
	}
	return parseForm(p.Data.JobPosting.ApplicationForm)
}

// form_sponsorship.json is the live form of a posting that asks a Boolean
// "require sponsorship", a Boolean right-to-work, a select and an optional essay
// (fetched 2026-10-27, see testdata/README.md).
func TestParseFormSponsorshipFixture(t *testing.T) {
	f := formFromFixture(t, "form_sponsorship.json")
	if f == nil {
		t.Fatal("form not read")
	}
	if f.V != posting.ApplyFormVersion || f.Questions != 8 || f.Required != 5 || f.Essays != 0 || len(f.Uploads) != 0 || f.Incomplete || f.Omitted != 0 {
		t.Fatalf("counts %+v", f)
	}
	if len(f.Gates) != 2 {
		t.Fatalf("gates %+v", f.Gates)
	}
	elig, spons := f.Gates[0], f.Gates[1]
	if elig.Kind != posting.GateWorkAuth || !elig.YesNo || elig.Flag != posting.FlagEligibilityYesNo ||
		!strings.HasPrefix(elig.Text, "Are you living in the country where this role is based") {
		t.Fatalf("eligibility gate %+v", elig)
	}
	// "Will you ... require sponsorship": Yes is an allowed answer, so no flag.
	if spons.Kind != posting.GateSponsorship || !spons.YesNo || spons.Flag != "" || len(spons.Options) != 0 {
		t.Fatalf("sponsorship gate %+v", spons)
	}
}

func TestParseFormEssaysFixture(t *testing.T) {
	f := formFromFixture(t, "form_essays.json")
	if f == nil || f.Questions != 6 || f.Required != 6 || f.Essays != 4 || len(f.Gates) != 0 || len(f.Uploads) != 0 {
		t.Fatalf("%+v", f)
	}
}

func TestParseFormRequiredSponsorshipFixture(t *testing.T) {
	f := formFromFixture(t, "form_required_sponsorship.json")
	if f == nil || f.Questions != 7 || f.Required != 6 || f.Essays != 3 || len(f.Gates) != 1 || f.Gates[0].Kind != posting.GateSponsorship {
		t.Fatalf("%+v", f)
	}
}

// field builds one fieldEntries member.
func field(path, title, typ string, required bool, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	t, _ := json.Marshal(title)
	return fmt.Sprintf(`{"isRequired":%t,"field":{"path":%q,"title":%s,"type":%q%s}}`, required, path, t, typ, extra)
}

func formOf(entries ...string) json.RawMessage {
	return json.RawMessage(`{"sections":[{"title":"s","fieldEntries":[` + strings.Join(entries, ",") + `]}]}`)
}

func selectValues(labels ...string) string {
	var vs []string
	for _, l := range labels {
		b, _ := json.Marshal(l)
		vs = append(vs, fmt.Sprintf(`{"label":%s,"value":%s,"isArchived":false}`, b, b))
	}
	return `"selectableValues":[` + strings.Join(vs, ",") + `]`
}

func TestParseFormKnockOuts(t *testing.T) {
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("_systemfield_email", "Email", "Email", true, ""),
		field("_systemfield_resume", "Resume", "File", true, ""),
		field("u1", "Cover Letter", "File", true, ""),
		field("u2", "Transcript", "File", false, ""),
		field("q1", "Do you now or in the future require sponsorship?", "ValueSelect", true,
			selectValues("No", "Yes, now", "Yes, in the future")),
		field("q2", "What is your current work authorization?", "ValueSelect", true,
			selectValues("US citizen", "Green card holder")),
		field("q3", "Which state do you live in?", "ValueSelect", true, selectValues("California", "Texas", "New York")),
		field("q4", "How many years of experience do you have with Go?", "ValueSelect", true, selectValues("0-2", "3-5", "6+")),
		field("q5", "What is your highest level of education?", "ValueSelect", true, selectValues("Bachelor's", "Master's", "PhD")),
		field("q6", "Tell us about a project", "LongText", true, ""),
		field("q7", "Anything else?", "LongText", false, ""),
		field("q8", "Start date", "Date", true, ""),
	))
	if f == nil {
		t.Fatal("not read")
	}
	if f.Questions != 10 || f.Required != 8 || f.Essays != 1 || len(f.Uploads) != 1 || f.Uploads[0] != "Cover Letter" {
		t.Fatalf("%+v", f)
	}
	kinds := map[string]posting.FormGate{}
	for _, g := range f.Gates {
		kinds[g.Kind] = g
	}
	if len(f.Gates) != 5 {
		t.Fatalf("gates %+v", f.Gates)
	}
	if g := kinds[posting.GateSponsorship]; g.Flag != "" || len(g.Options) != 3 {
		t.Fatalf("a list that offers a Yes is not flagged: %+v", g)
	}
	if g := kinds[posting.GateWorkAuth]; g.Flag != "" || len(g.Options) != 2 {
		t.Fatalf("work authorization select %+v", g)
	}
	if g := kinds[posting.GatePlace]; g.Flag != posting.FlagPlaceList || len(g.Options) != 3 {
		t.Fatalf("place %+v", g)
	}
	if g := kinds[posting.GateExperience]; len(g.Options) != 3 {
		t.Fatalf("experience %+v", g)
	}
	if g := kinds[posting.GateDegree]; len(g.Options) != 3 {
		t.Fatalf("degree %+v", g)
	}
}

func TestParseFormNoSponsorshipAnswer(t *testing.T) {
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("q1", "Will you require visa sponsorship?", "ValueSelect", true,
			selectValues("I am a US citizen", "I hold a green card", "I have an EAD")),
	))
	if f == nil || len(f.Gates) != 1 || f.Gates[0].Flag != posting.FlagNoSponsorAnswer {
		t.Fatalf("%+v", f)
	}
}

func TestParseFormUnknownTypesAreCountedNotClassified(t *testing.T) {
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("a", "Sponsorship?", "Hologram", true, ""),
		field("b", "Anything", "Score", false, ""),
	))
	if f == nil || f.Questions != 2 || f.Required != 1 || len(f.Gates) != 0 || f.Incomplete {
		t.Fatalf("%+v", f)
	}
}

func TestParseFormHugeOptionListIsBounded(t *testing.T) {
	labels := make([]string, 5000)
	for i := range labels {
		labels[i] = fmt.Sprintf("Option %d %s", i, strings.Repeat("x", 500))
	}
	labels[0] = "No, I do not need sponsorship"
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("q", "Will you need visa sponsorship?", "ValueSelect", true, selectValues(labels...)),
	))
	if f == nil || len(f.Gates) != 1 {
		t.Fatalf("%+v", f)
	}
	g := f.Gates[0]
	if len(g.Options) != posting.MaxFormOptions || g.OptionsTotal != 5000 {
		t.Fatalf("options %d total %d", len(g.Options), g.OptionsTotal)
	}
	for _, o := range g.Options {
		if n := len([]rune(o)); n > posting.MaxFormOptionRunes {
			t.Fatalf("option of %d runes", n)
		}
	}
	// Nothing in the kept or the unkept options says sponsorship is needed.
	if g.Flag != posting.FlagNoSponsorAnswer {
		t.Fatalf("flag %q", g.Flag)
	}
}

func TestParseFormArchivedOptionsAreLeftOut(t *testing.T) {
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("q", "Which country are you in?", "ValueSelect", true,
			`"selectableValues":[{"label":"France","value":"fr"},{"label":"Spain","value":"es","isArchived":true},{"label":"","value":"Italy"}]`),
	))
	if f == nil || len(f.Gates) != 1 || fmt.Sprint(f.Gates[0].Options) != "[France Italy]" {
		t.Fatalf("%+v", f)
	}
}

func TestParseFormUnicodeAndHostileLabels(t *testing.T) {
	long := strings.Repeat("é", 500)
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("q", "Müssen Sie ein Visum benötigen? visa‮ sponsorship\x00\n\t"+long, "Boolean", true, ""),
		field("u", "Zeugnis​ für � das Abitur", "File", true, ""),
	))
	if f == nil || len(f.Gates) != 1 || len(f.Uploads) != 1 {
		t.Fatalf("%+v", f)
	}
	g := f.Gates[0]
	if g.Kind != posting.GateSponsorship || len([]rune(g.Text)) > posting.MaxFormLabelRunes || !strings.HasSuffix(g.Text, "…") {
		t.Fatalf("%q", g.Text)
	}
	if strings.ContainsAny(g.Text, "‮\x00\n\t") || strings.Contains(f.Uploads[0], "​") {
		t.Fatalf("control or format characters survived: %q %q", g.Text, f.Uploads[0])
	}
}

func TestParseFormMissingLabels(t *testing.T) {
	f := parseForm(formOf(
		field("_systemfield_name", "Name", "String", true, ""),
		field("a", "", "Boolean", true, ""),
		field("b", "   ", "File", true, ""),
		`{"isRequired":true,"field":{"path":"c","type":"Boolean"}}`,
	))
	if f == nil || f.Questions != 3 || f.Required != 3 || len(f.Gates) != 0 || len(f.Uploads) != 1 || f.Uploads[0] != "File upload" {
		t.Fatalf("%+v", f)
	}
}

func TestParseFormUploadAndGateBoundsAreCarriedAsOmitted(t *testing.T) {
	var es []string
	es = append(es, field("_systemfield_name", "Name", "String", true, ""))
	for i := 0; i < 7; i++ {
		es = append(es, field(fmt.Sprintf("u%d", i), fmt.Sprintf("Upload %d", i), "File", true, ""))
	}
	for i := 0; i < 20; i++ {
		es = append(es, field(fmt.Sprintf("b%d", i), fmt.Sprintf("Are you eligible to work in country %d?", i), "Boolean", true, ""))
	}
	f := parseForm(formOf(es...))
	if f == nil || len(f.Uploads) != posting.MaxFormUploads || len(f.Gates) != posting.MaxFormGates || f.Omitted != 3+8 || f.Required != 27 {
		t.Fatalf("%+v", f)
	}
}

func TestParseFormScanBounds(t *testing.T) {
	var es []string
	for i := 0; i < maxFormEntries+25; i++ {
		es = append(es, field(fmt.Sprintf("q%d", i), "Question", "String", true, ""))
	}
	f := parseForm(formOf(es...))
	if f == nil || !f.Incomplete || f.Questions != maxFormEntries {
		t.Fatalf("entries bound: %+v", f)
	}
	var secs []string
	for i := 0; i < maxFormSections+5; i++ {
		secs = append(secs, `{"fieldEntries":[`+field("q", "Q", "String", true, "")+`]}`)
	}
	f = parseForm(json.RawMessage(`{"sections":[` + strings.Join(secs, ",") + `]}`))
	if f == nil || !f.Incomplete || f.Questions != maxFormSections {
		t.Fatalf("sections bound: %+v", f)
	}
}

// A form that is absent, null, empty or in a shape this reader does not know is
// "not read" (nil), never a form with no knock-outs.
func TestParseFormNotReadShapes(t *testing.T) {
	cases := map[string]string{
		"absent":           ``,
		"null":             `null`,
		"empty object":     `{}`,
		"sections null":    `{"sections":null}`,
		"sections empty":   `{"sections":[]}`,
		"empty entries":    `{"sections":[{"fieldEntries":[]}]}`,
		"entries null":     `{"sections":[{"fieldEntries":null}]}`,
		"sections object":  `{"sections":{"fieldEntries":[]}}`,
		"entries object":   `{"sections":[{"fieldEntries":{"a":1}}]}`,
		"form is a string": `"form"`,
		"form is an array": `[]`,
		"malformed json":   `{"sections":[`,
		"all entries bad":  `{"sections":[{"fieldEntries":[{},"x",{"field":"y"},{"isRequired":"yes","field":{"type":"String"}},{"field":{}}]}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if f := parseForm(json.RawMessage(raw)); f != nil {
				t.Fatalf("read as %+v", f)
			}
		})
	}
}

func TestParseFormBadEntriesMakeItIncomplete(t *testing.T) {
	f := parseForm(json.RawMessage(`{"sections":[{"fieldEntries":[` +
		field("q", "Are you eligible to work here?", "Boolean", true, "") + `,{},"x",{"isRequired":"yes","field":{"type":"String"}}]}]}`))
	if f == nil || !f.Incomplete || f.Questions != 1 || len(f.Gates) != 1 {
		t.Fatalf("%+v", f)
	}
}

// --- the job-board (fallback) path: the form rides the detail request.

func detailWithForm(id string, form string) string {
	return gqlDetail(`{"id":"` + id + `","title":"T","isListed":true,"descriptionHtml":"<p>d</p>","applicationForm":` + form + `}`)
}

func TestJobBoardReadsTheFormInTheDetailRequest(t *testing.T) {
	raw, err := os.ReadFile("testdata/form_sponsorship.json")
	if err != nil {
		t.Fatal(err)
	}
	var p postingPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	s, d := gqlDoer(gqlList(`[{"id":"a","title":"T"}]`, ""), map[string]srctest.Reply{
		"a": {Status: 200, Body: []byte(detailWithForm("a", string(p.Data.JobPosting.ApplicationForm)))},
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Form == nil || got[0].Form.Questions != 8 || len(got[0].Form.Gates) != 2 {
		t.Fatalf("form %+v", got[0].Form)
	}
	// One request per posting, as before: the form is in the same query.
	reqs := 0
	for _, r := range d.Requests() {
		if r.URL == graphQLURL(jobPostingOp) {
			reqs++
			if !strings.Contains(string(r.Body), "applicationForm") || !strings.Contains(string(r.Body), "descriptionHtml") {
				t.Fatalf("detail query %s", r.Body)
			}
		}
	}
	if reqs != 1 {
		t.Fatalf("%d detail requests", reqs)
	}
}

func TestJobBoardFormNotReadKeepsThePosting(t *testing.T) {
	for name, form := range map[string]string{
		"null":          `null`,
		"changed shape": `{"sections":"later"}`,
		"empty":         `{"sections":[]}`,
		"string":        `"x"`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := gqlDoer(gqlList(`[{"id":"a","title":"T"}]`, ""), map[string]srctest.Reply{
				"a": {Status: 200, Body: []byte(detailWithForm("a", form))},
			})
			got, err := s.List(context.Background(), board)
			if err != nil || len(got) != 1 {
				t.Fatalf("%v %v", got, err)
			}
			if got[0].Form != nil || got[0].Description != "d" {
				t.Fatalf("form %+v description %q", got[0].Form, got[0].Description)
			}
		})
	}
}

// A partial GraphQL error (the form resolver failing) beside a posting keeps
// the posting and reads no form.
func TestJobBoardFormErrorBesideAPostingKeepsIt(t *testing.T) {
	body := `{"data":{"jobPosting":{"id":"a","title":"T","isListed":true,"descriptionHtml":"<p>d</p>","applicationForm":null}},"errors":[{"message":"form unavailable","path":["jobPosting","applicationForm"]}]}`
	s, d := gqlDoer(gqlList(`[{"id":"a","title":"T"}]`, ""), map[string]srctest.Reply{"a": {Status: 200, Body: []byte(body)}})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form != nil {
		t.Fatalf("%v %v", got, err)
	}
	if n := countPosts(d, jobPostingOp); n != 1 {
		t.Fatalf("%d detail requests", n)
	}
}

func countPosts(d *srctest.Doer, op string) int {
	n := 0
	for _, r := range d.Requests() {
		if r.URL == graphQLURL(op) {
			n++
		}
	}
	return n
}

// A reply that errors with no posting at all may be the form's doing: the
// detail is asked once more without the form, and the posting still lists.
func TestJobBoardRetriesWithoutTheForm(t *testing.T) {
	list := gqlList(`[{"id":"a","title":"T"}]`, "")
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case boardURL:
			return srctest.Reply{Status: 404, Body: []byte(`{}`)}, true
		case graphQLURL(jobBoardOp):
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		case graphQLURL(jobPostingOp):
			if strings.Contains(string(r.Body), "applicationForm") {
				return srctest.Reply{Status: 200, Body: []byte(`{"data":{"jobPosting":null},"errors":[{"message":"Cannot return null for non-nullable field"}]}`)}, true
			}
			return srctest.Reply{Status: 200, Body: []byte(gqlDetail(jobJSON("a", "T", "", "", "", "<p>d</p>", "", true)))}, true
		}
		return srctest.Reply{}, false
	})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form != nil || got[0].Description != "d" {
		t.Fatalf("%v %v", got, err)
	}
	if n := countPosts(d, jobPostingOp); n != 2 {
		t.Fatalf("%d detail requests, want the form query and the one without", n)
	}
}

func TestJobBoardRetryThatStillFailsFailsTheBoard(t *testing.T) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case boardURL:
			return srctest.Reply{Status: 404, Body: []byte(`{}`)}, true
		case graphQLURL(jobBoardOp):
			return srctest.Reply{Status: 200, Body: []byte(gqlList(`[{"id":"a","title":"T"}]`, ""))}, true
		case graphQLURL(jobPostingOp):
			return srctest.Reply{Status: 200, Body: []byte(`{"data":{"jobPosting":null},"errors":[{"message":"boom"}]}`)}, true
		}
		return srctest.Reply{}, false
	})
	s := &source{d: d, now: func() time.Time { return fixedNow }}
	got, err := s.List(context.Background(), board)
	if err == nil || got != nil || !strings.Contains(err.Error(), "boom") || errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("%v %v", got, err)
	}
	if n := countPosts(d, jobPostingOp); n != 2 {
		t.Fatalf("%d detail requests", n)
	}
}

// An HTTP failure is not retried without the form (that would double the load
// on an outage) and fails the board as it always did.
func TestJobBoardHTTPFailureIsNotRetried(t *testing.T) {
	s, d := gqlDoer(gqlList(`[{"id":"a","title":"T"}]`, ""), map[string]srctest.Reply{"a": {Status: 500, Body: []byte(`{}`)}})
	if got, err := s.List(context.Background(), board); err == nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if n := countPosts(d, jobPostingOp); n != 1 {
		t.Fatalf("%d detail requests", n)
	}
}

// --- the posting-API path: the form is a separate, bounded request.

func apiBody(ids ...string) string {
	var js []string
	for _, id := range ids {
		js = append(js, fmt.Sprintf(`{"id":%q,"title":"T %s","jobUrl":"https://x.test/%s","isListed":true,"descriptionPlain":"text of %s"}`, id, id, id, id))
	}
	return `{"jobs":[` + strings.Join(js, ",") + `]}`
}

const sponsorForm = `{"sections":[{"fieldEntries":[
{"isRequired":true,"field":{"path":"_systemfield_name","title":"Name","type":"String"}},
{"isRequired":true,"field":{"path":"q","title":"Are you legally authorized to work in the US without sponsorship?","type":"Boolean"}}]}]}`

type formDoer struct {
	mu       sync.Mutex
	body     string
	forms    map[string]srctest.Reply // by posting id
	posts    []string                 // ids asked, in order
	bodies   []string
	onPost   func(id string)
	fallback srctest.Reply
}

func (f *formDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case boardURL:
			return srctest.Reply{Status: 200, Body: []byte(f.body)}, true
		case graphQLURL(jobPostingOp):
			var q struct {
				Variables struct {
					ID string `json:"jobPostingId"`
				} `json:"variables"`
			}
			if json.Unmarshal(r.Body, &q) != nil {
				return srctest.Reply{}, false
			}
			f.mu.Lock()
			f.posts = append(f.posts, q.Variables.ID)
			f.bodies = append(f.bodies, string(r.Body))
			f.mu.Unlock()
			if f.onPost != nil {
				f.onPost(q.Variables.ID)
			}
			if rep, ok := f.forms[q.Variables.ID]; ok {
				return rep, true
			}
			return f.fallback, f.fallback.Status != 0
		}
		return srctest.Reply{}, false
	})
	return d.Do(ctx, req)
}

func formReply(id, form string) srctest.Reply {
	return srctest.Reply{Status: 200, Body: []byte(`{"data":{"jobPosting":{"id":"` + id + `","applicationForm":` + form + `}}}`)}
}

func newFormSource(t *testing.T, fd egress.Doer, o FormOptions) *source {
	t.Helper()
	if o.Known == nil {
		o.Known = func(posting.Key) (posting.Posting, bool) { return posting.Posting{}, false }
	}
	src, err := NewWithForms(fd, o)
	if err != nil {
		t.Fatal(err)
	}
	s := src.(*source)
	s.now = func() time.Time { return fixedNow }
	return s
}

func TestNewWithFormsNeedsKnown(t *testing.T) {
	if _, err := NewWithForms(srctest.New(nil), FormOptions{}); err == nil {
		t.Fatal("want an error without Known")
	}
}

// New(d) reads no forms: the posting API list costs one request and carries no
// summary, as before.
func TestPlainSourceReadsNoForms(t *testing.T) {
	s, d := newSource(one(200, apiBody("a", "b")))
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 2 || got[0].Form != nil || got[1].Form != nil {
		t.Fatalf("%v %v", got, err)
	}
	if urls := d.URLs(); len(urls) != 1 {
		t.Fatalf("urls %v", urls)
	}
}

func TestFormsAreReadPerNewPosting(t *testing.T) {
	fd := &formDoer{body: apiBody("a", "b"), forms: map[string]srctest.Reply{
		"a": formReply("a", sponsorForm), "b": formReply("b", `null`),
	}}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{Report: func(_ sources.Board, st FormStats) { stats = st }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Form == nil || len(got[0].Form.Gates) != 1 || got[0].Form.Gates[0].Flag != posting.FlagAuthorizedYesNo {
		t.Fatalf("a: %+v", got[0].Form)
	}
	if got[1].Form != nil {
		t.Fatalf("b: a null form must stay not read, got %+v", got[1].Form)
	}
	if stats.Fetched != 1 || stats.Unread != 1 || stats.Reused != 0 || stats.Failed != 0 {
		t.Fatalf("%+v", stats)
	}
	for _, b := range fd.bodies {
		if strings.Contains(b, "descriptionHtml") || !strings.Contains(b, "applicationForm") {
			t.Fatalf("the form query asks for more or less than the form: %s", b)
		}
	}
	raw, _ := json.Marshal(stats)
	if strings.Contains(string(raw), "authorized") {
		t.Fatalf("stats carry question text: %s", raw)
	}
}

func TestStoredFormIsReusedWhileTheTextIsUnchanged(t *testing.T) {
	fd := &formDoer{body: apiBody("a", "b", "c"), forms: map[string]srctest.Reply{
		"a": formReply("a", sponsorForm), "b": formReply("b", sponsorForm), "c": formReply("c", sponsorForm),
	}}
	stored := map[posting.Key]posting.Posting{}
	// a: same text, form read. b: same text, form not read. c: text changed.
	for id, form := range map[string]*posting.ApplyForm{"a": {V: 1, Questions: 9}, "b": nil, "c": {V: 1, Questions: 9}} {
		text := "text of " + id
		if id == "c" {
			text = "older text"
		}
		stored[posting.Key("ab:ashby:"+id)] = posting.Posting{Description: text, Form: form}
	}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{
		Known:  func(k posting.Key) (posting.Posting, bool) { p, ok := stored[k]; return p, ok },
		Report: func(_ sources.Board, st FormStats) { stats = st },
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 3 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Form == nil || got[0].Form.Questions != 9 {
		t.Fatalf("a was not reused: %+v", got[0].Form)
	}
	if got[1].Form == nil || got[1].Form.Questions != 1 || got[2].Form == nil || got[2].Form.Questions != 1 {
		t.Fatalf("b, c: %+v %+v", got[1].Form, got[2].Form)
	}
	if fmt.Sprint(fd.posts) != "[b c]" || stats.Reused != 1 || stats.Fetched != 2 {
		t.Fatalf("asked %v, %+v", fd.posts, stats)
	}
}

func TestStoredFormWithEvictedTextIsReused(t *testing.T) {
	fd := &formDoer{body: apiBody("a")}
	s := newFormSource(t, fd, FormOptions{Known: func(posting.Key) (posting.Posting, bool) {
		return posting.Posting{Form: &posting.ApplyForm{V: 1, Questions: 3}}, true
	}})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form == nil || len(fd.posts) != 0 {
		t.Fatalf("%v %v asked %v", got, err, fd.posts)
	}
}

func TestStoredFormOfAnotherVersionIsNotReused(t *testing.T) {
	fd := &formDoer{body: apiBody("a"), forms: map[string]srctest.Reply{"a": formReply("a", sponsorForm)}}
	s := newFormSource(t, fd, FormOptions{Known: func(posting.Key) (posting.Posting, bool) {
		return posting.Posting{Form: &posting.ApplyForm{V: 99, Questions: 3}}, true
	}})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form == nil || got[0].Form.V != posting.ApplyFormVersion || len(fd.posts) != 1 {
		t.Fatalf("%v %v asked %v", got, err, fd.posts)
	}
}

func TestFormReadFailureKeepsThePostingAndTheBoard(t *testing.T) {
	cases := map[string]srctest.Reply{
		"500":          {Status: 500, Body: []byte(`{}`)},
		"not json":     {Status: 200, Body: []byte(`<html>`)},
		"graphql err":  {Status: 200, Body: []byte(`{"errors":[{"message":"rate limited"}]}`)},
		"id mismatch":  formReply("other", sponsorForm),
		"403":          {Status: 403, Body: []byte(`{}`)},
		"truncated":    {Status: 200, Body: []byte(`{"data":{"jobPosting":{"id":"a","applicationForm":{"sections":[`)},
		"empty object": {Status: 200, Body: []byte(`{}`)},
	}
	for name, rep := range cases {
		t.Run(name, func(t *testing.T) {
			fd := &formDoer{body: apiBody("a", "b"), fallback: rep}
			var stats FormStats
			s := newFormSource(t, fd, FormOptions{Report: func(_ sources.Board, st FormStats) { stats = st }})
			got, err := s.List(context.Background(), board)
			if err != nil || len(got) != 2 {
				t.Fatalf("a failed form read must not fail the board: %v %v", got, err)
			}
			for _, p := range got {
				if p.Form != nil || p.Description == "" {
					t.Fatalf("posting not left intact: %+v", p)
				}
			}
			if stats.Fetched != 0 || stats.Failed+stats.NotFound+stats.Unread != 2 {
				t.Fatalf("%+v", stats)
			}
		})
	}
}

func TestFormReadNotFoundIsNotAFailure(t *testing.T) {
	var ids []string
	for i := 0; i < maxFormFailures+3; i++ {
		ids = append(ids, fmt.Sprintf("p%d", i))
	}
	fd := &formDoer{body: apiBody(ids...), fallback: srctest.Reply{Status: 404, Body: []byte(`{}`)}}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{Report: func(_ sources.Board, st FormStats) { stats = st }})
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if stats.NotFound != len(ids) || stats.Failed != 0 || stats.Skipped != 0 || len(fd.posts) != len(ids) {
		t.Fatalf("%+v asked %d", stats, len(fd.posts))
	}
}

func TestFormFailureLimitStopsAskingTheBoard(t *testing.T) {
	var ids []string
	for i := 0; i < 20; i++ {
		ids = append(ids, fmt.Sprintf("p%d", i))
	}
	fd := &formDoer{body: apiBody(ids...), fallback: srctest.Reply{Status: 500, Body: []byte(`{}`)}}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{Report: func(_ sources.Board, st FormStats) { stats = st }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 20 {
		t.Fatalf("%d %v", len(got), err)
	}
	if len(fd.posts) != maxFormFailures || stats.Failed != maxFormFailures || stats.Skipped != 20-maxFormFailures || stats.FirstError == "" {
		t.Fatalf("asked %d, %+v", len(fd.posts), stats)
	}
}

func TestFormBoardCap(t *testing.T) {
	var ids []string
	forms := map[string]srctest.Reply{}
	for i := 0; i < MaxFormsPerBoard+15; i++ {
		id := fmt.Sprintf("p%d", i)
		ids = append(ids, id)
		forms[id] = formReply(id, sponsorForm)
	}
	fd := &formDoer{body: apiBody(ids...), forms: forms}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{Report: func(_ sources.Board, st FormStats) { stats = st }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != len(ids) {
		t.Fatalf("%d %v", len(got), err)
	}
	read := 0
	for _, p := range got {
		if p.Form != nil {
			read++
		}
	}
	if len(fd.posts) != MaxFormsPerBoard || read != MaxFormsPerBoard || stats.Skipped != 15 || stats.Fetched != MaxFormsPerBoard {
		t.Fatalf("asked %d read %d %+v", len(fd.posts), read, stats)
	}
}

func TestFormRunBudgetIsSharedAcrossBoards(t *testing.T) {
	fd := &formDoer{body: apiBody("a", "b", "c"), forms: map[string]srctest.Reply{
		"a": formReply("a", sponsorForm), "b": formReply("b", sponsorForm), "c": formReply("c", sponsorForm),
	}}
	s := newFormSource(t, fd, FormOptions{})
	s.forms.left.Store(2)
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), board); err != nil {
		t.Fatal(err)
	}
	if len(fd.posts) != 2 {
		t.Fatalf("asked %v, the run budget is 2", fd.posts)
	}
}

func TestFormSkipIsNeverAsked(t *testing.T) {
	fd := &formDoer{body: apiBody("a", "b"), forms: map[string]srctest.Reply{"a": formReply("a", sponsorForm), "b": formReply("b", sponsorForm)}}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{
		Skip:   func(p posting.Posting) bool { return p.Title == "T a" },
		Report: func(_ sources.Board, st FormStats) { stats = st },
	})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 2 || got[0].Form != nil || got[1].Form == nil || fmt.Sprint(fd.posts) != "[b]" || stats.Prefiltered != 1 {
		t.Fatalf("%v %v asked %v %+v", got, err, fd.posts, stats)
	}
}

func TestFormCancelStopsTheBoard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fd := &formDoer{body: apiBody("a", "b", "c"), forms: map[string]srctest.Reply{"a": formReply("a", sponsorForm)},
		onPost: func(string) { cancel() }, fallback: srctest.Reply{Status: 500, Body: []byte(`{}`)}}
	s := newFormSource(t, fd, FormOptions{})
	got, err := s.List(ctx, board)
	if err == nil || got != nil || len(fd.posts) != 1 {
		t.Fatalf("%v %v asked %v", got, err, fd.posts)
	}
}

func TestFormRequestTimeoutIsBounded(t *testing.T) {
	old := formRequestTimeout
	t.Cleanup(func() { formRequestTimeout = old })
	formRequestTimeout = 30 * time.Millisecond
	d := &hangingDoer{board: apiBody("a")}
	s := newFormSource(t, d, FormOptions{})
	start := time.Now()
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form != nil {
		t.Fatalf("%v %v", got, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the form request was not bounded")
	}
}

// hangingDoer answers the posting API and never answers a form request.
type hangingDoer struct{ board string }

func (h *hangingDoer) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if req.URL.String() == boardURL {
		return srctest.New(map[string]srctest.Reply{boardURL: {Status: 200, Body: []byte(h.board)}}).Do(ctx, req)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestFormBodyCap(t *testing.T) {
	old := maxFormBody
	t.Cleanup(func() { maxFormBody = old })
	maxFormBody = 64
	fd := &formDoer{body: apiBody("a"), forms: map[string]srctest.Reply{"a": formReply("a", sponsorForm)}}
	var stats FormStats
	s := newFormSource(t, fd, FormOptions{Report: func(_ sources.Board, st FormStats) { stats = st }})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form != nil || stats.Failed != 1 {
		t.Fatalf("%v %v %+v", got, err, stats)
	}
	if !strings.Contains(stats.FirstError, srcutil.ErrBodyTooLarge.Error()) {
		t.Fatalf("first error %q", stats.FirstError)
	}
}

// The form read is also what the fallback list uses, so a board served by the
// job-board GraphQL never double-reads: NewWithForms leaves that path alone.
func TestFormsSourceFallbackPathReadsFormsInTheDetail(t *testing.T) {
	list := gqlList(`[{"id":"a","title":"T"}]`, "")
	d := srctest.NewFunc(func(r srctest.Request) (srctest.Reply, bool) {
		switch r.URL {
		case boardURL:
			return srctest.Reply{Status: 404, Body: []byte(`{}`)}, true
		case graphQLURL(jobBoardOp):
			return srctest.Reply{Status: 200, Body: []byte(list)}, true
		case graphQLURL(jobPostingOp):
			return srctest.Reply{Status: 200, Body: []byte(detailWithForm("a", sponsorForm))}, true
		}
		return srctest.Reply{}, false
	})
	s := newFormSource(t, d, FormOptions{})
	got, err := s.List(context.Background(), board)
	if err != nil || len(got) != 1 || got[0].Form == nil {
		t.Fatalf("%v %v", got, err)
	}
	if n := countPosts(d, jobPostingOp); n != 1 {
		t.Fatalf("%d detail requests", n)
	}
}
