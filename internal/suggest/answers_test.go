package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

// scriptAnswerer answers a page's batch with fn and records what it was asked.
type scriptAnswerer struct {
	mu   sync.Mutex
	seen []draft.AnswerRequest
	fn   func(r draft.AnswerRequest) (draft.AnswerResult, error)
}

func (s *scriptAnswerer) Answer(_ context.Context, r draft.AnswerRequest) (draft.AnswerResult, error) {
	s.mu.Lock()
	s.seen = append(s.seen, r)
	s.mu.Unlock()
	return s.fn(r)
}

// answersFrom is an Answerer that gives question k the answer as[k].
func answersFrom(as ...draft.Answer) *scriptAnswerer {
	return &scriptAnswerer{fn: func(r draft.AnswerRequest) (draft.AnswerResult, error) {
		return draft.AnswerResult{Answers: as, Model: "claude-test", Turns: 1}, nil
	}}
}

func choiceField(id, label string, opts ...string) fieldtriage.Field {
	return fieldtriage.Field{ID: id, Label: label, Kind: fieldtriage.KindChoice, Options: opts}
}

func factField(id, label string) fieldtriage.Field {
	return fieldtriage.Field{ID: id, Label: label, Kind: fieldtriage.KindText}
}

func TestRunAnswersFactsAndChoicesInOneBatch(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		"Why Acme?":                     answered(fieldtriage.Essay),
		"Years of Python?":              answered(fieldtriage.Fact),
		"Willing to relocate?":          answered(fieldtriage.Fact),
		"Which stack do you prefer?":    answered(fieldtriage.Other),
		"Employment type you want?":     answered(fieldtriage.WorkAuth), // read as pinned: stays manual
		"Describe the project you led.": answered(fieldtriage.Other),    // text, other: manual as before
	}}
	dr := &fakeDrafter{}
	an := answersFrom(
		draft.Answer{Text: "4 years", EvidenceItems: 2},
		draft.Answer{Choice: "Yes", EvidenceItems: 1},
		draft.Answer{Code: draft.CodeNoEvidence, Reason: draft.NoEvidenceReason},
	)
	req := Request{Job: job, Fields: []fieldtriage.Field{
		text("e", "Why Acme?"),
		factField("f", "Years of Python?"),
		choiceField("c1", "Willing to relocate?", "Yes", "No"),
		choiceField("pin", "Are you legally authorized to work in the US?", "Yes", "No"),
		choiceField("c2", "Which stack do you prefer?", "Go", "Rust"),
		choiceField("wa", "Employment type you want?", "Full time", "Contract"),
		text("o", "Describe the project you led."),
	}}
	out, err := Engine{D: d, Drafter: dr, Answerer: an}.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(an.seen) != 1 {
		t.Fatalf("Answerer called %d times, want once for the page", len(an.seen))
	}
	var asked []string
	for _, q := range an.seen[0].Questions {
		asked = append(asked, q.Label+fmt.Sprint(q.Options))
	}
	if got := strings.Join(asked, "|"); got != "Years of Python?[]|Willing to relocate?[Yes No]|Which stack do you prefer?[Go Rust]" {
		t.Errorf("asked %s", got)
	}
	if an.seen[0].Job != job {
		t.Errorf("job = %+v", an.seen[0].Job)
	}
	if dr.calls.Load() != 1 {
		t.Errorf("%d essay drafts, want 1: a choice or fact is never drafted as an essay", dr.calls.Load())
	}
	type row struct{ status, kind, draft, choice, code string }
	want := map[string]row{
		"e":   {Drafted, AnswerEssay, "draft for Why Acme?", "", ""},
		"f":   {Drafted, AnswerFact, "4 years", "", ""},
		"c1":  {Drafted, AnswerChoice, "", "Yes", ""},
		"pin": {Manual, "", "", "", ""},
		"c2":  {Manual, "", "", "", draft.CodeNoEvidence},
		"wa":  {Manual, "", "", "", ""},
		"o":   {Manual, "", "", "", ""},
	}
	for _, s := range out {
		w := want[s.ID]
		if s.Status != w.status || s.AnswerKind != w.kind || s.Draft != w.draft || s.Choice != w.choice || s.Code != w.code {
			t.Errorf("%s: %+v, want %+v", s.ID, s, w)
		}
		if s.Status == Drafted && s.ID != "e" && (!strings.Contains(s.Reason, "yellow") || s.Model != "claude-test" || s.EvidenceItems == 0) {
			t.Errorf("%s: a drafted answer should say it is filled yellow and carry its model and evidence: %+v", s.ID, s)
		}
	}
	if out[4].Reason != draft.NoEvidenceReason {
		t.Errorf("no_evidence reason = %q", out[4].Reason)
	}
	if out[3].Category != fieldtriage.WorkAuth || out[3].Reason == "" {
		t.Errorf("the pinned choice: %+v", out[3])
	}
	for _, l := range d.asked {
		if fieldtriage.Guard(l) != "" {
			t.Errorf("guarded label %q reached the decision model", l)
		}
	}
}

// The reply carries exactly the contract's keys: answer_kind on a drafted row,
// choice only on a drafted choice row, neither on anything else.
func TestSuggestionWireShape(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Years?": answered(fieldtriage.Fact), "Relocate?": answered(fieldtriage.Fact), "Pronouns note": answered(fieldtriage.Other)}}
	an := answersFrom(draft.Answer{Text: "4 years", EvidenceItems: 1}, draft.Answer{Choice: "No", EvidenceItems: 1})
	out, err := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: an}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{
		factField("a", "Years?"), choiceField("b", "Relocate?", "Yes", "No"), factField("c", "Pronouns note"), choiceField("d", "Gender", "Man", "Woman"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	has := func(i int, k string) bool { _, ok := rows[i][k]; return ok }
	if rows[0]["answer_kind"] != "fact" || rows[0]["draft"] != "4 years" || has(0, "choice") {
		t.Errorf("fact row = %v", rows[0])
	}
	if rows[1]["answer_kind"] != "choice" || rows[1]["choice"] != "No" || has(1, "draft") {
		t.Errorf("choice row = %v", rows[1])
	}
	for _, i := range []int{2, 3} {
		if has(i, "answer_kind") || has(i, "choice") || has(i, "draft") {
			t.Errorf("row %d drafted nothing but carries answer fields: %v", i, rows[i])
		}
	}
}

func TestRunCapsAnswersPerPage(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{}}
	var fields []fieldtriage.Field
	for i := range MaxAnswers + 3 {
		l := fmt.Sprintf("Fact %d", i)
		d.byLabel[l] = answered(fieldtriage.Fact)
		fields = append(fields, factField(fmt.Sprint(i), l))
	}
	an := &scriptAnswerer{fn: func(r draft.AnswerRequest) (draft.AnswerResult, error) {
		res := draft.AnswerResult{Model: "m", Turns: 1}
		for range r.Questions {
			res.Answers = append(res.Answers, draft.Answer{Text: "x", EvidenceItems: 1})
		}
		return res, nil
	}}
	out, err := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: an}.Run(context.Background(), Request{Job: job, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	if len(an.seen) != 1 || len(an.seen[0].Questions) != MaxAnswers || MaxAnswers != 15 {
		t.Fatalf("%d turns, %d questions in the first", len(an.seen), len(an.seen[0].Questions))
	}
	for i, s := range out {
		if i < MaxAnswers && s.Status != Drafted || i >= MaxAnswers && (s.Status != Skipped || s.Code != CodeAnswerCap) {
			t.Errorf("field %d: %s/%s", i, s.Status, s.Code)
		}
	}
}

func TestEachAnswerOutcomeMapsToAStatusAndCode(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"A?": answered(fieldtriage.Fact), "B?": answered(fieldtriage.Fact), "C?": answered(fieldtriage.Fact), "D?": answered(fieldtriage.Fact), "E?": answered(fieldtriage.Fact)}}
	an := answersFrom(
		draft.Answer{Code: draft.CodeUngrounded, Kind: draft.KindYears, Reason: "the answer says 9 years"},
		draft.Answer{Code: draft.CodeBadReply, Reason: "bad"},
		draft.Answer{Code: draft.CodeTooLarge, Reason: "big"},
		draft.Answer{Choice: "Maybe"}, // not one of the options: the engine re-checks
		draft.Answer{Text: strings.Repeat("x", 11)},
	)
	fields := []fieldtriage.Field{factField("a", "A?"), factField("b", "B?"), factField("c", "C?"), choiceField("d", "D?", "Yes", "No"), factField("e", "E?")}
	fields[4].MaxChars = 10
	out, err := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: an}.Run(context.Background(), Request{Job: job, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ status, code, kind string }{
		{Manual, draft.CodeUngrounded, draft.KindYears}, {Failed, draft.CodeBadReply, ""}, {Failed, draft.CodeTooLarge, ""},
		{Failed, draft.CodeBadReply, ""}, {Failed, CodeTooLong, ""},
	}
	for i, w := range want {
		if s := out[i]; s.Status != w.status || s.Code != w.code || s.Kind != w.kind || s.Draft != "" || s.Choice != "" || s.AnswerKind != "" {
			t.Errorf("%s: %+v, want %+v", s.ID, s, w)
		}
	}
}

func TestAFailedBatchFailsItsFieldsAndNothingElse(t *testing.T) {
	for code, err := range map[string]error{
		draft.CodeBudget:   &draft.Error{Code: draft.CodeBudget, Turns: 1, Err: errors.New("spent")},
		draft.CodeProvider: errors.New("503"),
		draft.CodeKB:       &draft.Error{Code: draft.CodeKB, Err: errors.New("kb")},
		draft.CodeTimeout:  &draft.Error{Code: draft.CodeTimeout, Turns: 1, Err: errors.New("slow")},
	} {
		d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Why?": answered(fieldtriage.Essay), "A?": answered(fieldtriage.Fact), "B?": answered(fieldtriage.Fact)}}
		an := &scriptAnswerer{fn: func(draft.AnswerRequest) (draft.AnswerResult, error) { return draft.AnswerResult{}, err }}
		var rec Outcome
		out, rerr := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: an, Record: func(o Outcome) { rec = o }}.Run(context.Background(),
			Request{Job: job, Fields: []fieldtriage.Field{text("e", "Why?"), factField("a", "A?"), choiceField("b", "B?", "Yes", "No")}})
		if rerr != nil {
			t.Fatal(rerr)
		}
		if out[0].Status != Drafted {
			t.Errorf("%s: the essay was lost with the batch: %+v", code, out[0])
		}
		for _, s := range out[1:] {
			if s.Status != Failed || s.Code != code || s.Reason == "" || s.Draft != "" || s.Choice != "" {
				t.Errorf("%s: %s = %+v", code, s.ID, s)
			}
		}
		if rec.AnswerAsked != 2 || rec.AnswerAnswered != 0 {
			t.Errorf("%s: outcome %+v", code, rec)
		}
	}
}

// A budget that an essay already spent, or a knowledge base that did not load,
// refuses the batch without a call.
func TestBatchIsNotAskedAfterTheBudgetIsSpent(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Why?": answered(fieldtriage.Essay), "A?": answered(fieldtriage.Fact)}}
	for _, code := range []string{draft.CodeBudget, draft.CodeKB} {
		dr := &fakeDrafter{err: &draft.Error{Code: code, Err: errors.New("stop")}}
		an := answersFrom(draft.Answer{Text: "x"})
		out, err := Engine{D: d, Drafter: dr, Answerer: an}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{text("e", "Why?"), factField("a", "A?")}})
		if err != nil {
			t.Fatal(err)
		}
		if len(an.seen) != 0 {
			t.Errorf("%s: the batch was asked after the stop", code)
		}
		if out[1].Status != Failed || out[1].Code != code {
			t.Errorf("%s: %+v", code, out[1])
		}
	}
}

func TestWithoutAnAnswererFactsAndChoicesStayManual(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"A?": answered(fieldtriage.Fact), "B?": answered(fieldtriage.Essay)}}
	dr := &fakeDrafter{}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{factField("a", "A?"), choiceField("b", "B?", "Yes", "No")}})
	if err != nil {
		t.Fatal(err)
	}
	if dr.calls.Load() != 0 || out[0].Status != Manual || out[1].Status != Manual {
		t.Errorf("%+v", out)
	}
	for _, s := range out {
		if s.Reason == "" {
			t.Errorf("%s is manual with no reason", s.ID)
		}
	}
}

// Whatever the decision model reads, a label a sensitive pattern flags is never
// batched, and neither is a choice read as work authorization, self-identification
// or consent.
func TestPinnedFieldsAreNeverBatched(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		"Date of birth":                 answered(fieldtriage.Fact),
		"Passport number":               answered(fieldtriage.Fact),
		"Employment preference":         answered(fieldtriage.SelfID),
		"Which team?":                   answered(fieldtriage.Consent),
		"Preferred start":               answered(fieldtriage.WorkAuth),
		"Anything about your ethnicity": answered(fieldtriage.Other),
	}}
	an := answersFrom()
	out, err := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: an}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{
		factField("1", "Date of birth"), choiceField("2", "Passport number", "A", "B"), choiceField("3", "Employment preference", "A", "B"),
		choiceField("4", "Which team?", "A", "B"), choiceField("5", "Preferred start", "A", "B"), choiceField("6", "Anything about your ethnicity", "A", "B"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(an.seen) != 0 {
		t.Errorf("a pinned field was batched: %+v", an.seen)
	}
	for _, s := range out {
		if s.Status != Manual {
			t.Errorf("%s: %s", s.ID, s.Status)
		}
	}
}

func TestOutcomeCountsTheBatchAndKindsWithoutText(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Why?": answered(fieldtriage.Essay), "A-SECRET?": answered(fieldtriage.Fact), "B-SECRET?": answered(fieldtriage.Fact)}}
	an := answersFrom(draft.Answer{Text: "SECRET answer", EvidenceItems: 1}, draft.Answer{Code: draft.CodeNoEvidence, Reason: "SECRET"})
	var rec Outcome
	_, err := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: an, Record: func(o Outcome) { rec = o }}.Run(context.Background(),
		Request{Job: job, Fields: []fieldtriage.Field{text("e", "Why?"), factField("a", "A-SECRET?"), choiceField("b", "B-SECRET?", "Yes", "No")}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.AnswerAsked != 2 || rec.AnswerAnswered != 1 || rec.AnswerTurns != 1 {
		t.Errorf("batch counts: %+v", rec)
	}
	var kinds []string
	for _, f := range rec.Fields {
		kinds = append(kinds, f.AnswerKind)
	}
	if got := strings.Join(kinds, ","); got != "essay,fact,choice" {
		t.Errorf("answer kinds %s", got)
	}
	line, err := rec.line()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(line)), "secret") {
		t.Errorf("text reached the outcome line: %s", line)
	}
}

func TestChoiceFieldsOnTheWire(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"job":{"company":"Acme"},"fields":[{"id":"a","label":"Relocate?","kind":"choice","options":["Yes","No"]},{"id":"b","label":"Why?","kind":"textarea"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	if err := req.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"options on text": `{"fields":[{"id":"a","label":"Why?","kind":"text","options":["Yes","No"]}]}`,
		"one option":      `{"fields":[{"id":"a","label":"Why?","kind":"choice","options":["Yes"]}]}`,
		"no options":      `{"fields":[{"id":"a","label":"Why?","kind":"choice"}]}`,
		"max_chars":       `{"fields":[{"id":"a","label":"Why?","kind":"choice","max_chars":5,"options":["Yes","No"]}]}`,
	} {
		var r Request
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(name, err)
		}
		if err := r.Validate(); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: err = %v, want ErrBadRequest", name, err)
		}
	}
}
