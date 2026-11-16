package decide

import (
	"errors"
	"strings"
	"testing"
)

func okQuestion() Question {
	return Question{
		Task: "t", Prompt: "p",
		Slots: []Slot{{Name: "a", Options: []string{"x", "y"}}},
	}
}

func TestValidateQuestion(t *testing.T) {
	manyOpts := func(n int) []string {
		o := make([]string, n)
		for i := range o {
			o[i] = string(rune('a' + i))
		}
		return o
	}
	cases := []struct {
		name string
		mut  func(*Question)
		bad  bool
	}{
		{"ok", func(*Question) {}, false},
		{"empty task", func(q *Question) { q.Task = "" }, true},
		{"empty prompt", func(q *Question) { q.Prompt = "" }, true},
		{"no slots", func(q *Question) { q.Slots = nil }, true},
		{"one option", func(q *Question) { q.Slots[0].Options = []string{"x"} }, true},
		{"zero options", func(q *Question) { q.Slots[0].Options = nil }, true},
		{"16 options", func(q *Question) { q.Slots[0].Options = manyOpts(16) }, false},
		{"17 options", func(q *Question) { q.Slots[0].Options = manyOpts(17) }, true},
		{"empty option", func(q *Question) { q.Slots[0].Options = []string{"x", ""} }, true},
		{"duplicate option", func(q *Question) { q.Slots[0].Options = []string{"x", "x"} }, true},
		{"empty slot name", func(q *Question) { q.Slots[0].Name = "" }, true},
		{"duplicate slot", func(q *Question) { q.Slots = append(q.Slots, q.Slots[0]) }, true},
		{"context at cap", func(q *Question) { q.Context = make([]byte, MaxContextBytes) }, false},
		{"context over cap", func(q *Question) { q.Context = make([]byte, MaxContextBytes+1) }, true},
		{"option at cap", func(q *Question) { q.Slots[0].Options = []string{strings.Repeat("o", 512), "y"} }, false},
		{"option over cap", func(q *Question) { q.Slots[0].Options = []string{strings.Repeat("o", 513), "y"} }, true},
		{"prompt at cap", func(q *Question) { q.Prompt = strings.Repeat("p", 4096) }, false},
		{"prompt over cap", func(q *Question) { q.Prompt = strings.Repeat("p", 4097) }, true},
		{"task at cap", func(q *Question) { q.Task = strings.Repeat("t", 256) }, false},
		{"task over cap", func(q *Question) { q.Task = strings.Repeat("t", 257) }, true},
		{"multibyte option measured in bytes", func(q *Question) {
			q.Slots[0].Options = []string{strings.Repeat("é", 257), "y"} // 514 bytes
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := okQuestion()
			c.mut(&q)
			err := ValidateQuestion(q)
			if (err != nil) != c.bad {
				t.Fatalf("err = %v, want bad=%v", err, c.bad)
			}
			if err != nil && !errors.Is(err, ErrInvalidQuestion) {
				t.Fatalf("error does not wrap ErrInvalidQuestion: %v", err)
			}
		})
	}
}

func TestValidateErrorDoesNotEchoContext(t *testing.T) {
	q := okQuestion()
	q.Context = []byte(strings.Repeat("SECRET", MaxContextBytes))
	err := ValidateQuestion(q)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckValue(t *testing.T) {
	s := Slot{Name: "a", Options: []string{"yes", "no"}}
	for _, v := range []string{"yes", "no"} {
		if err := checkValue(s, v); err != nil {
			t.Errorf("%q rejected: %v", v, err)
		}
	}
	for _, v := range []string{"", "Yes", " yes", "yes ", "maybe", "yes\n", "yes,no"} {
		if err := checkValue(s, v); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("%q accepted or wrong error: %v", v, err)
		}
	}
}
