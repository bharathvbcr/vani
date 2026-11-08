package contacts

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jarvisio"
	"github.com/bharathvbcr/decide"
)

const secret = "contact-secret-example-9f3a"

type mem struct {
	keys []string
	text map[string]string
	err  error
	gets int
	list int
}

func (m *mem) List(context.Context) ([]string, error) {
	m.list++
	if m.err != nil {
		return nil, m.err
	}
	return append([]string(nil), m.keys...), nil
}

func (m *mem) Get(_ context.Context, key string) (string, error) {
	m.gets++
	if m.err != nil {
		return "", m.err
	}
	t, ok := m.text[key]
	if !ok {
		return "", &jarvisio.Error{Code: jarvisio.CodeJarvis, Message: "inputs value is not stored"}
	}
	return t, nil
}

type ask struct {
	rd   decide.SlotReading
	err  error
	saw  []decide.Question
	hits int
}

func (a *ask) Ask(_ context.Context, q decide.Question) (decide.SlotReading, error) {
	a.hits++
	a.saw = append(a.saw, q)
	return a.rd, a.err
}

func field() Field { return Field{Label: "Portfolio URL", Kind: "text"} }

func answered(v string) decide.SlotReading {
	return decide.SlotReading{Reading: decide.Answered, Value: v}
}

func TestEmptyCatalogDoesNotAsk(t *testing.T) {
	store := &mem{}
	route, claude := &ask{}, &ask{}
	got := Choose(context.Background(), field(), store, route, claude)
	if got.Status != Empty || route.hits != 0 || claude.hits != 0 || store.gets != 0 {
		t.Fatalf("%+v route %d claude %d gets %d", got, route.hits, claude.hits, store.gets)
	}
	if got.Text != "" || got.Status == Filled {
		t.Fatalf("empty catalog looked filled: %+v", got)
	}
}

func TestServeDownIsNotRun(t *testing.T) {
	store := &mem{err: &jarvisio.Error{Code: jarvisio.CodeUnavailable, Message: "jarvis executable is missing"}}
	route := &ask{rd: answered("website")}
	got := Choose(context.Background(), field(), store, route, &ask{})
	if got.Status != NotRun || got.Note != NotRunNote || route.hits != 0 || got.Text != "" {
		t.Fatalf("%+v hits %d", got, route.hits)
	}
}

func TestBankMissingIsNotAnEmptyCatalog(t *testing.T) {
	store := &mem{keys: []string{"website"}, text: map[string]string{"website": secret},
		err: &jarvisio.Error{Code: jarvisio.CodeJarvis, Message: "inputs read failed: bank executable is missing"}}
	route := &ask{rd: answered("website")}
	got := Choose(context.Background(), field(), store, route, &ask{})
	if got.Status != NotRun || got.Status == Empty || got.Status == Filled {
		t.Fatalf("bank missing looked like empty or filled: %+v", got)
	}
	if !strings.Contains(got.Note, "bank executable is missing") || got.Note == NotRunNote && !strings.Contains(got.Note, "bank") {
		t.Fatalf("note %q", got.Note)
	}
	if route.hits != 0 || strings.Contains(got.Note, secret) || got.Text != "" {
		t.Fatalf("asked or leaked: %+v hits %d", got, route.hits)
	}
}

func TestAReplyThatIsNotAKeyIsRefused(t *testing.T) {
	store := &mem{keys: []string{"email", "website"}, text: map[string]string{"website": secret}}
	route := &ask{rd: answered("Ada Lovelace")}
	claude := &ask{rd: answered("website")}
	got := Choose(context.Background(), field(), store, route, claude)
	if got.Status != Refused || claude.hits != 0 || store.gets != 0 || got.Text != "" {
		t.Fatalf("%+v claude %d gets %d", got, claude.hits, store.gets)
	}
	if strings.Contains(questionText(route), secret) {
		t.Fatal("the value reached the decision question")
	}
}

func TestAbstainThenClaudeFetchesTheKey(t *testing.T) {
	store := &mem{keys: []string{"website", "email"}, text: map[string]string{"website": secret}}
	route := &ask{rd: decide.SlotReading{Reading: decide.Abstained}}
	claude := &ask{rd: answered("website")}
	got := Choose(context.Background(), field(), store, route, claude)
	if got.Status != Filled || got.Key != "website" || !got.Review || got.Text != secret || store.gets != 1 || claude.hits != 1 {
		t.Fatalf("%+v gets %d claude %d", got, store.gets, claude.hits)
	}
	for _, who := range []*ask{route, claude} {
		q := questionText(who)
		if strings.Contains(q, secret) || strings.Contains(q, "Ada") {
			t.Fatalf("prompt holds a value: %s", q)
		}
		if !strings.Contains(q, "website") || !strings.Contains(q, "email") || !strings.Contains(q, Descriptions["website"]) {
			t.Fatalf("prompt missing keys or descriptions: %s", q)
		}
		if strings.Contains(q, "phone") {
			t.Fatal("a key that is not stored was offered")
		}
	}
}

func TestClaudeProseIsRefused(t *testing.T) {
	store := &mem{keys: []string{"email"}, text: map[string]string{"email": secret}}
	route := &ask{rd: decide.SlotReading{Reading: decide.Abstained}}
	claude := &ask{rd: answered("probably the email address")}
	got := Choose(context.Background(), field(), store, route, claude)
	if got.Status != Refused || store.gets != 0 || got.Text != "" {
		t.Fatalf("%+v", got)
	}
}

func TestNoneDoesNotAskClaude(t *testing.T) {
	store := &mem{keys: []string{"email"}}
	route := &ask{rd: answered(None)}
	claude := &ask{}
	got := Choose(context.Background(), field(), store, route, claude)
	if got.Status != NoneHit || claude.hits != 0 || store.gets != 0 || got.Text != "" {
		t.Fatalf("%+v claude %d", got, claude.hits)
	}
}

func TestDecisionUnavailableDoesNotAskClaude(t *testing.T) {
	store := &mem{keys: []string{"email"}, text: map[string]string{"email": secret}}
	route := &ask{rd: decide.SlotReading{Reading: decide.Unavailable, Detail: "no_binary"}}
	claude := &ask{rd: answered("email")}
	got := Choose(context.Background(), field(), store, route, claude)
	if got.Status != NotRun || got.Note != NotRunNote || claude.hits != 0 || got.Text != "" || got.Status == Filled {
		t.Fatalf("%+v claude %d", got, claude.hits)
	}
}

func TestBlockedLabelsAreNotAsked(t *testing.T) {
	store := &mem{keys: []string{"email"}, text: map[string]string{"email": secret}}
	route := &ask{rd: answered("email")}
	for _, f := range []Field{
		{Label: "Will you now or in the future require visa sponsorship?", Kind: "text"},
		{Label: "I agree to the privacy policy", Kind: "text"},
		{Label: "Gender", Kind: "text"},
		{Label: "Date of birth", Kind: "text"},
		{Label: "National ID", Kind: "text"},
		{Label: "Account password", Kind: "text"},
		{Label: "Anything", Kind: "password"},
	} {
		got := Choose(context.Background(), f, store, route, &ask{})
		if got.Status != Skipped || route.hits != 0 || store.list != 0 {
			t.Fatalf("%q: %+v hits %d list %d", f.Label, got, route.hits, store.list)
		}
	}
}

func TestGetFailureAfterAKeyIsNotAFill(t *testing.T) {
	store := &mem{keys: []string{"website"}, err: nil}
	store.text = nil
	route := &ask{rd: answered("website")}
	got := Choose(context.Background(), field(), store, route, &ask{})
	if got.Status != Missing || got.Text != "" || got.Status == Filled {
		t.Fatalf("%+v", got)
	}
	store.err = &jarvisio.Error{Code: jarvisio.CodeJarvis, Message: "inputs read failed: bank executable is missing"}
	store.gets = 0
	got = Choose(context.Background(), field(), store, route, &ask{})
	if got.Status != NotRun || !strings.Contains(got.Note, "bank executable is missing") || got.Text != "" {
		t.Fatalf("get bank-missing = %+v", got)
	}
}

func TestQuestionRejectsAValueShapedOption(t *testing.T) {
	q, err := Question(field(), []string{"website", secret})
	if err != nil {
		t.Fatal(err)
	}
	blob := q.Prompt + string(q.Context)
	for _, s := range q.Slots[0].Options {
		blob += " " + s
	}
	if strings.Contains(blob, secret) {
		t.Fatal("a non-key was offered to the model")
	}
	if _, err := Question(Field{Label: "", Kind: "text"}, []string{"email"}); err == nil {
		t.Fatal("empty label accepted")
	}
}

func questionText(a *ask) string {
	if len(a.saw) == 0 {
		return ""
	}
	q := a.saw[0]
	s := q.Prompt + "\n" + string(q.Context)
	for _, slot := range q.Slots {
		for _, o := range slot.Options {
			s += "\n" + o
			if d, ok := Descriptions[o]; ok {
				s += " " + d
			}
		}
	}
	return s
}

func TestCanceledListIsNotRun(t *testing.T) {
	store := &mem{err: context.Canceled}
	got := Choose(context.Background(), field(), store, &ask{}, &ask{})
	if got.Status != NotRun || !errors.Is(context.Canceled, context.Canceled) || got.Text != "" {
		t.Fatalf("%+v", got)
	}
}
