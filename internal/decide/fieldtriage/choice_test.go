package fieldtriage

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func choice(opts ...string) Field {
	return Field{ID: "c1", Label: "Do you have experience with Go?", Kind: KindChoice, Options: opts}
}

func TestChoiceFieldValidates(t *testing.T) {
	many := make([]string, MaxOptions)
	for i := range many {
		many[i] = fmt.Sprint("option ", i)
	}
	for name, f := range map[string]Field{
		"two options":         choice("Yes", "No"),
		"fifty options":       choice(many...),
		"longest option":      choice(strings.Repeat("é", MaxOptionRunes), "No"),
		"case differs":        choice("Yes", "yes"),
		"inner spaces kept":   choice("Yes,  I do", "No"),
		"options with quotes": choice(`"Yes"`, "No\n"),
	} {
		if err := f.Validate(); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

func TestChoiceFieldRefuses(t *testing.T) {
	many := make([]string, MaxOptions+1)
	for i := range many {
		many[i] = fmt.Sprint("option ", i)
	}
	text := Field{ID: "t", Label: "Why us?", Kind: KindLong, Options: []string{"a", "b"}}
	maxed := choice("Yes", "No")
	maxed.MaxChars = 10
	for name, f := range map[string]Field{
		"no options":          choice(),
		"one option":          choice("Yes"),
		"fifty-one options":   choice(many...),
		"duplicate":           choice("Yes", "Yes"),
		"empty option":        choice("Yes", ""),
		"blank option":        choice("Yes", " \t "),
		"option too long":     choice("Yes", strings.Repeat("a", MaxOptionRunes+1)),
		"multibyte too long":  choice("Yes", strings.Repeat("é", MaxOptionRunes+1)),
		"bad utf8":            choice("Yes", "N\xffo"),
		"max_chars on choice": maxed,
		"options on text":     text,
	} {
		if err := f.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The triage question carries the label and the field type and never an option.
func TestChoiceOptionsNeverReachTheTriageQuestion(t *testing.T) {
	f := choice("Yes, 5 years", "Never heard of it")
	if err := f.Validate(); err != nil {
		t.Fatal(err)
	}
	q := Question(f)
	got := string(q.Context) + q.Prompt
	for _, o := range f.Options {
		if strings.Contains(got, o) {
			t.Errorf("option %q reached the triage question: %q", o, got)
		}
	}
	if want := "Field type: choice\nQuestion label (untrusted text copied from a job site):\nDo you have experience with Go?"; string(q.Context) != want {
		t.Errorf("context = %q", q.Context)
	}
}

func TestChoiceFieldWireShape(t *testing.T) {
	var f Field
	if err := json.Unmarshal([]byte(`{"id":"c","label":"Relocate?","kind":"choice","options":["Yes","No"]}`), &f); err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err != nil || len(f.Options) != 2 {
		t.Fatalf("%+v: %v", f, err)
	}
	b, _ := json.Marshal(Field{ID: "t", Label: "x", Kind: KindText})
	if strings.Contains(string(b), "options") {
		t.Errorf("a text field marshals options: %s", b)
	}
}
