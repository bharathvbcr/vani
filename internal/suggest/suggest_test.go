package suggest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

// fakeDecider answers by label from a table; a missing label errors.
type fakeDecider struct {
	mu      sync.Mutex
	byLabel map[string]decide.SlotReading
	asked   []string
	delay   time.Duration
}

func (f *fakeDecider) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	label := string(q.Context[strings.LastIndex(string(q.Context), "\n")+1:])
	f.mu.Lock()
	f.asked = append(f.asked, label)
	r, ok := f.byLabel[label]
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return decide.Answer{}, ctx.Err()
		}
	}
	if !ok {
		return decide.Answer{}, errors.New("backend down")
	}
	return decide.Answer{Backend: "fake", Slots: map[string]decide.SlotReading{fieldtriage.Slot: r}}, nil
}

type fakeDrafter struct {
	calls atomic.Int32
	err   error
	seen  []draft.Request
}

func (f *fakeDrafter) Draft(_ context.Context, r draft.Request) (draft.Result, error) {
	f.calls.Add(1)
	f.seen = append(f.seen, r)
	if f.err != nil {
		return draft.Result{}, f.err
	}
	return draft.Result{Draft: "draft for " + r.Question, Model: "claude-test", EvidenceItems: 2}, nil
}

func answered(v string) decide.SlotReading { return decide.SlotReading{Reading: decide.Answered, Value: v} }

var job = draft.Job{Company: "Acme", Title: "Engineer", Description: strings.Repeat("Build reliable systems. ", 5)}

func text(id, label string) fieldtriage.Field {
	return fieldtriage.Field{ID: id, Label: label, Kind: fieldtriage.KindLong}
}

func TestRunDraftsOnlyEssaysAndNeverAsksGuardedLabels(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		"Why Acme?":              answered(fieldtriage.Essay),
		"Additional Information": answered(fieldtriage.Essay),
		"GitHub URL":             answered(fieldtriage.Fact),
		"Something odd":          {Reading: decide.Abstained},
	}}
	dr := &fakeDrafter{}
	req := Request{Job: job, Fields: []fieldtriage.Field{
		text("a", "Why Acme?"),
		text("b", "Will you require visa sponsorship?"),
		text("c", "GitHub URL"),
		text("d", "Additional Information"),
		text("e", "Agreement to Arbitrate"),
		text("f", "Something odd"),
		text("g", "Not in the table"),
	}}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": Drafted, "b": Manual, "c": Manual, "d": Drafted, "e": Manual, "f": Unsure, "g": Unsure}
	for i, s := range out {
		if s.ID != req.Fields[i].ID {
			t.Fatalf("order changed at %d: %s", i, s.ID)
		}
		if s.Status != want[s.ID] {
			t.Errorf("%s: status %s (%s), want %s", s.ID, s.Status, s.Reason, want[s.ID])
		}
		if s.Status != Drafted && s.Draft != "" {
			t.Errorf("%s: has a draft without status drafted", s.ID)
		}
	}
	for _, l := range d.asked {
		if fieldtriage.Guard(l) != "" {
			t.Errorf("guarded label %q reached the decision model", l)
		}
	}
	if dr.calls.Load() != 2 {
		t.Fatalf("drafter called %d times, want 2", dr.calls.Load())
	}
	if out[1].Category != fieldtriage.WorkAuth || out[4].Category != fieldtriage.Consent {
		t.Fatalf("guard categories: %+v / %+v", out[1], out[4])
	}
}

func TestRunCapsDraftsPerPage(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{}}
	var fields []fieldtriage.Field
	for i := range 6 {
		l := fmt.Sprintf("Essay %d", i)
		d.byLabel[l] = answered(fieldtriage.Essay)
		fields = append(fields, text(fmt.Sprint(i), l))
	}
	dr := &fakeDrafter{}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if dr.calls.Load() != 2 {
		t.Fatalf("%d drafts, want 2", dr.calls.Load())
	}
	if out[0].Status != Drafted || out[1].Status != Drafted || out[2].Status != Skipped || out[5].Status != Skipped {
		t.Fatalf("statuses: %+v", out)
	}
}

func TestRunStopsDraftingWhenBudgetIsSpent(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"E1": answered(fieldtriage.Essay), "E2": answered(fieldtriage.Essay)}}
	dr := &fakeDrafter{err: &draft.Error{Code: draft.CodeBudget, Err: errors.New("spent")}}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{text("1", "E1"), text("2", "E2")}})
	if err != nil {
		t.Fatal(err)
	}
	if dr.calls.Load() != 1 {
		t.Fatalf("drafter called %d times after the budget was spent", dr.calls.Load())
	}
	for _, s := range out {
		if s.Status != Failed || s.Code != draft.CodeBudget {
			t.Fatalf("%s: %s/%s, want failed/budget_exhausted", s.ID, s.Status, s.Code)
		}
	}
}

func TestRunWithoutPostingDraftsNothing(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Why?": answered(fieldtriage.Essay)}}
	dr := &fakeDrafter{}
	out, err := Engine{D: d, Drafter: dr}.Run(context.Background(), Request{Job: draft.Job{Description: "short"}, Fields: []fieldtriage.Field{text("1", "Why?")}})
	if err != nil {
		t.Fatal(err)
	}
	if dr.calls.Load() != 0 || out[0].Status != Manual {
		t.Fatalf("drafted without a posting: %+v", out[0])
	}
}

func TestTriageTimeoutLeavesFieldsUnsure(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Slow": answered(fieldtriage.Essay)}, delay: time.Second}
	dr := &fakeDrafter{}
	out, err := Engine{D: d, Drafter: dr, Triage: 20 * time.Millisecond}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{text("1", "Slow")}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != Unsure || dr.calls.Load() != 0 {
		t.Fatalf("a timed-out triage was acted on: %+v", out[0])
	}
}

func TestOutOfOptionValueIsUnsure(t *testing.T) {
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{"Q": answered("allow")}}
	out, err := Engine{D: d, Drafter: &fakeDrafter{}}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{text("1", "Q")}})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Status != Unsure {
		t.Fatalf("value outside the options was acted on: %+v", out[0])
	}
}

func TestRunRefusesBadRequests(t *testing.T) {
	many := make([]fieldtriage.Field, MaxFields+1)
	for i := range many {
		many[i] = text(fmt.Sprint(i), "Q")
	}
	for name, req := range map[string]Request{
		"no fields":    {Job: job},
		"too many":     {Job: job, Fields: many},
		"duplicate id": {Job: job, Fields: []fieldtriage.Field{text("1", "A"), text("1", "B")}},
		"empty label":  {Job: job, Fields: []fieldtriage.Field{text("1", "  ")}},
		"choice kind":  {Job: job, Fields: []fieldtriage.Field{{ID: "1", Label: "Q", Kind: "radio"}}},
		"draft cap":    {Job: job, Fields: []fieldtriage.Field{text("1", "Q")}, MaxDrafts: MaxDrafts + 1},
		"long title":   {Job: draft.Job{Title: strings.Repeat("t", draft.MaxTitleRunes+1)}, Fields: []fieldtriage.Field{text("1", "Q")}},
	} {
		d := &fakeDecider{}
		_, err := Engine{D: d, Drafter: &fakeDrafter{}}.Run(context.Background(), req)
		if !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: err = %v, want ErrBadRequest", name, err)
		}
		if len(d.asked) != 0 {
			t.Errorf("%s: asked the decision model before validating", name)
		}
	}
	if _, err := (Engine{}).Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{text("1", "Q")}}); err == nil {
		t.Error("ran without a decider or drafter")
	}
}
