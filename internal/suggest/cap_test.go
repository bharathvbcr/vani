package suggest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

// scriptDrafter answers call n (from 1) with fn(n, request).
type scriptDrafter struct {
	mu    sync.Mutex
	calls int
	seen  []draft.Request
	fn    func(n int, r draft.Request) (draft.Result, error)
}

func (s *scriptDrafter) Draft(_ context.Context, r draft.Request) (draft.Result, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.seen = append(s.seen, r)
	s.mu.Unlock()
	return s.fn(n, r)
}

func ok(_ int, r draft.Request) (draft.Result, error) {
	return draft.Result{Draft: "draft for " + r.Question, Model: "claude-test", EvidenceItems: 1, Turns: 1}, nil
}

func essays(n int) (*fakeDecider, []fieldtriage.Field) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{}}
	var fs []fieldtriage.Field
	for i := range n {
		l := fmt.Sprintf("Essay %d", i)
		d.byLabel[l] = answered(fieldtriage.Essay)
		fs = append(fs, text(fmt.Sprint(i), l))
	}
	return d, fs
}

func statuses(out []Suggestion) string {
	var b strings.Builder
	for _, s := range out {
		b.WriteString(s.Status[:1])
	}
	return b.String()
}

// A draft that fails does not use up the page's cap: only drafts that came back count.
func TestFailedDraftsDoNotConsumeThePageCap(t *testing.T) {
	d, fields := essays(6)
	dr := &scriptDrafter{fn: func(n int, r draft.Request) (draft.Result, error) {
		if n <= 2 {
			return draft.Result{}, &draft.Error{Code: draft.CodeUngrounded, Kind: draft.KindFigure, Turns: 2, Err: errors.New("no")}
		}
		return ok(n, r)
	}}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: 2})
	if err != nil {
		t.Fatal(err)
	}
	drafted := 0
	for _, s := range out {
		if s.Status == Drafted {
			drafted++
		}
	}
	if drafted != 2 || dr.calls != 4 {
		t.Fatalf("%d drafted from %d calls (%s), want 2 from 4", drafted, dr.calls, statuses(out))
	}
	if statuses(out) != "ffddss" {
		t.Errorf("statuses = %s", statuses(out))
	}
	if out[0].Turns != 2 || out[2].Turns != 1 {
		t.Errorf("turns not carried: %d, %d", out[0].Turns, out[2].Turns)
	}
}

// A provider that fails every draft is still bounded: MaxDrafts+ExtraAttempts tries.
func TestAFailingProviderIsBoundedByTheAttemptCap(t *testing.T) {
	for _, maxDrafts := range []int{1, 3, MaxDrafts} {
		d, fields := essays(MaxFields)
		dr := &scriptDrafter{fn: func(int, draft.Request) (draft.Result, error) {
			return draft.Result{}, &draft.Error{Code: draft.CodeProvider, Err: errors.New("503")}
		}}
		out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: maxDrafts})
		if err != nil {
			t.Fatal(err)
		}
		if want := maxDrafts + ExtraAttempts; dr.calls != want {
			t.Errorf("max_drafts %d: %d calls, want %d", maxDrafts, dr.calls, want)
		}
		last := out[len(out)-1]
		if last.Status != Skipped || last.Code != CodeAttempts {
			t.Errorf("an untried essay is %s/%s, want skipped/%s", last.Status, last.Code, CodeAttempts)
		}
	}
}

func TestAnUnreadableKnowledgeBaseFailsEssaysWithoutRetryingEach(t *testing.T) {
	d, fields := essays(4)
	dr := &scriptDrafter{fn: func(int, draft.Request) (draft.Result, error) {
		return draft.Result{}, &draft.Error{Code: draft.CodeKB, Err: errors.New("kb: no selections")}
	}}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	if dr.calls != 1 {
		t.Errorf("%d calls after the knowledge base failed, want 1", dr.calls)
	}
	for _, s := range out {
		if s.Status != Failed || s.Code != draft.CodeKB || s.Category != fieldtriage.Essay {
			t.Errorf("%s: %s/%s/%s, want failed/kb with the essay category kept", s.ID, s.Status, s.Code, s.Category)
		}
	}
}

func TestDrafterThatPinsASensitiveCategoryLeavesTheFieldManual(t *testing.T) {
	d, fields := essays(1)
	dr := &scriptDrafter{fn: func(int, draft.Request) (draft.Result, error) {
		return draft.Result{}, &draft.Error{Code: draft.CodeNotDrafted, Category: fieldtriage.SelfID, Err: errors.New("no")}
	}}
	out, _ := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields})
	if out[0].Status != Manual || out[0].Category != fieldtriage.SelfID || out[0].Code != "" {
		t.Errorf("%+v", out[0])
	}
}

func TestWordsForMaxChars(t *testing.T) {
	for _, c := range []struct {
		chars, words int
		short        bool
	}{
		{0, 0, false}, // no limit: the drafter's default
		{-5, 0, false},
		{1, 0, true},
		{100, 0, true},
		{259, 0, true},               // 39 words: under MinWords
		{260, draft.MinWords, false}, // exactly MinWords
		{1000, 153, false},
		{1170, draft.DefaultWords, false}, // exactly DefaultWords
		{2600, draft.DefaultWords, false}, // a roomy box: no longer than no limit
		{100_000, draft.DefaultWords, false},
	} {
		w, short := WordsFor(c.chars)
		if w != c.words || short != c.short {
			t.Errorf("WordsFor(%d) = %d, %v; want %d, %v", c.chars, w, short, c.words, c.short)
		}
	}
}

func TestMaxCharsSetsTheDraftLengthAndShortFieldsAreManual(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		"Why us (500)": answered(fieldtriage.Essay), "Why us (100)": answered(fieldtriage.Essay), "Why us (none)": answered(fieldtriage.Essay),
		"Why us (huge)": answered(fieldtriage.Essay),
	}}
	f := func(id, label string, n int) fieldtriage.Field {
		return fieldtriage.Field{ID: id, Label: label, Kind: fieldtriage.KindLong, MaxChars: n}
	}
	dr := &scriptDrafter{fn: ok}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, MaxDrafts: MaxDrafts, Fields: []fieldtriage.Field{
		f("a", "Why us (500)", 500), f("b", "Why us (100)", 100), f("c", "Why us (none)", 0), f("d", "Why us (huge)", fieldtriage.MaxMaxChars),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out[1].Status != Manual || out[1].Reason != ShortAnswerReason || out[1].Draft != "" {
		t.Errorf("a 100 character field: %+v", out[1])
	}
	got := map[string]int{}
	for _, r := range dr.seen {
		got[r.Question] = r.MaxWords
	}
	if len(got) != 3 || got["Why us (500)"] != 76 || got["Why us (none)"] != 0 || got["Why us (huge)"] != draft.DefaultWords {
		t.Errorf("max_words sent = %v", got)
	}
}

func TestADraftOverTheFieldLimitLosesWholeSentencesOrFails(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
		ok   bool
	}{
		{"One. Two. Three.", 0, "One. Two. Three.", true},
		{"One. Two. Three.", 100, "One. Two. Three.", true},
		{"One. Two. Three.", 12, "One. Two.", true},
		{"One. Two. Three.", 4, "One.", true},
		{"One. Two. Three.", 3, "", false},
		{"No sentence end here at all", 10, "", false},
		{"Fine é. Next one here.", 8, "Fine é.", true},
		{"3.5 is not a stop. Next.", 5, "", false}, // a decimal point is not a sentence end
	} {
		got, ok := fit(c.in, c.max)
		if got != c.want || ok != c.ok {
			t.Errorf("fit(%q, %d) = %q, %v; want %q, %v", c.in, c.max, got, ok, c.want, c.ok)
		}
	}
	d, fields := essays(1)
	fields[0].MaxChars = 300
	long := strings.Repeat("word ", 100) // no sentence end
	dr := &scriptDrafter{fn: func(int, draft.Request) (draft.Result, error) { return draft.Result{Draft: long, Turns: 1}, nil }}
	out, _ := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields})
	if out[0].Status != Failed || out[0].Code != CodeTooLong || out[0].Draft != "" {
		t.Errorf("an overlong draft with no boundary: %+v", out[0])
	}
}

func TestRecordIsCalledOnceWithTheRunsCounts(t *testing.T) {
	d, fields := essays(3)
	fields = append(fields, text("g", "Will you require visa sponsorship?"))
	dr := &scriptDrafter{fn: func(n int, r draft.Request) (draft.Result, error) {
		if n == 1 {
			return draft.Result{}, &draft.Error{Code: draft.CodeTruncated, Err: errors.New("cut")}
		}
		return draft.Result{Draft: "SECRET DRAFT TEXT", Model: "claude-test", Turns: 2, Repaired: true}, nil
	}}
	var got []Outcome
	out, err := Engine{D: d, Drafter: dr, Record: func(o Outcome) { got = append(got, o) }}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: 2})
	if err != nil || len(out) != 4 {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Record called %d times", len(got))
	}
	o := got[0]
	if o.Op != OpSuggest || o.Attempts != 3 || o.Drafted != 2 || o.Cap != 2 || len(o.Fields) != 4 {
		t.Fatalf("outcome = %+v", o)
	}
	if f := o.Fields[0]; f.Status != Failed || f.Code != draft.CodeTruncated || f.Category != fieldtriage.Essay {
		t.Errorf("field 0 = %+v", f)
	}
	if f := o.Fields[1]; f.Status != Drafted || f.Turns != 2 || !f.Repaired || f.Model != "claude-test" || f.Backend != "fake" {
		t.Errorf("field 1 = %+v", f)
	}
	if f := o.Fields[3]; f.Status != Manual || f.Category != fieldtriage.WorkAuth {
		t.Errorf("field 3 = %+v", f)
	}
	// A bad request records nothing: nothing ran.
	got = nil
	_, err = Engine{D: d, Drafter: dr, Record: func(o Outcome) { got = append(got, o) }}.Run(context.Background(), Request{Job: job})
	if !errors.Is(err, ErrBadRequest) || len(got) != 0 {
		t.Errorf("bad request: err %v, recorded %d", err, len(got))
	}
}

// An essay the model read as one, whose wording trips the broad consent pattern
// ("agree"), is drafted; an agreement read as an essay is still left to the
// applicant (draft.NeverDrafted, the rule vani draft applies too).
func TestAnEssayWithAgreeInItIsDraftedAndAnAgreementIsNot(t *testing.T) {
	essay := "Describe a time a teammate did not agree with your approach."
	agreement := "Do you agree to the arbitration agreement?"
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{essay: answered(fieldtriage.Essay), agreement: answered(fieldtriage.Essay)}}
	dr := &scriptDrafter{fn: ok}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{
		{ID: "a", Label: essay, Kind: fieldtriage.KindLong}, {ID: "b", Label: agreement, Kind: fieldtriage.KindLong},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != Drafted {
		t.Errorf("the essay: %+v", out[0])
	}
	if out[1].Status != Manual || out[1].Category != fieldtriage.Consent {
		t.Errorf("the agreement: %+v", out[1])
	}
}
