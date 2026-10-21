package suggest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/decide"
)

// constDecider answers every label with one fixed reading.
type constDecider struct {
	val string
	err error
}

func (c constDecider) Decide(_ context.Context, _ decide.Question) (decide.Answer, error) {
	if c.err != nil {
		return decide.Answer{}, c.err
	}
	return decide.Answer{Backend: "fake", Slots: map[string]decide.SlotReading{fieldtriage.Slot: answered(c.val)}}, nil
}

func TestAttackPostModelDefence(t *testing.T) {
	sensitive := "Are you a U.S. citizen?"
	for _, val := range []string{"", "ESSAY", " essay", "essay ", "garbage", "drop table", "essay\n"} {
		dr := &fakeDrafter{}
		fields := []fieldtriage.Field{text("a", "Tell us about yourself"), text("b", "Why Acme?")}
		out, err := Engine{D: constDecider{val: val}, Drafter: dr}.Run(context.Background(), Request{Job: job, Fields: fields, MaxDrafts: MaxDrafts})
		if err != nil {
			t.Fatal(err)
		}
		if dr.calls.Load() != 0 {
			t.Errorf("value %q drafted %d times", val, dr.calls.Load())
		}
		for _, s := range out {
			if s.Status != Unsure && s.Status != Manual {
				t.Errorf("value %q: status %s", val, s.Status)
			}
		}
	}
	// A decider error never drafts.
	dr := &fakeDrafter{}
	out, _ := Engine{D: constDecider{err: errors.New("down")}, Drafter: dr}.Run(context.Background(),
		Request{Job: job, Fields: []fieldtriage.Field{text("a", "Why Acme?")}, MaxDrafts: MaxDrafts})
	if dr.calls.Load() != 0 || out[0].Status != Unsure {
		t.Errorf("decider error: status %s, drafts %d", out[0].Status, dr.calls.Load())
	}
	// A sensitive label answered "essay" by the model is never drafted.
	dr = &fakeDrafter{}
	out, _ = Engine{D: constDecider{val: fieldtriage.Essay}, Drafter: dr}.Run(context.Background(),
		Request{Job: job, Fields: []fieldtriage.Field{text("a", sensitive)}, MaxDrafts: MaxDrafts})
	if dr.calls.Load() != 0 || out[0].Draft != "" {
		t.Errorf("sensitive label drafted")
	}
}

// A label over the cap is refused before Sensitive or the model sees it, and a
// label at the cap is judged in bounded time.
func TestAttackLabelCapBoundsSensitive(t *testing.T) {
	dr := &fakeDrafter{}
	big := strings.Repeat("a ", 500000)
	_, err := Engine{D: constDecider{val: fieldtriage.Essay}, Drafter: dr}.Run(context.Background(),
		Request{Job: job, Fields: []fieldtriage.Field{text("a", big)}, MaxDrafts: MaxDrafts})
	if err == nil {
		t.Fatal("1 MB label was accepted")
	}
	for _, unit := range []string{"a ", "​", "&amp;", "citi­", "U "} {
		l := strings.Repeat(unit, fieldtriage.MaxLabelRunes)
		st := time.Now()
		_, err := Engine{D: constDecider{val: fieldtriage.Essay}, Drafter: dr}.Run(context.Background(),
			Request{Job: job, Fields: []fieldtriage.Field{text("a", l)}, MaxDrafts: MaxDrafts})
		if d := time.Since(st); d > 200*time.Millisecond {
			t.Errorf("unit %q at cap took %v (err %v)", unit, d, err)
		}
	}
}
