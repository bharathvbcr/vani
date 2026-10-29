package suggest

import (
	"context"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/decide"
)

// recProvider replies with one fixed text to every turn and records every request.
type recProvider struct {
	mu   sync.Mutex
	text string
	reqs []llm.Request
}

type recStream struct {
	text string
	done bool
}

func (s *recStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *recStream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}, StopReason: llm.StopEndTurn}, nil
}
func (s *recStream) Close() error { return nil }

func (p *recProvider) Name() string { return "fake" }
func (p *recProvider) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *recProvider) Stream(_ context.Context, req llm.Request) (llm.Stream, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
	return &recStream{text: p.text}, nil
}

// everything the provider was sent, system prompts and messages alike.
func (p *recProvider) sent() string {
	var b strings.Builder
	for _, r := range p.reqs {
		b.WriteString(r.System + "\n")
		for _, m := range r.Messages {
			b.WriteString(m.Text() + "\n")
		}
	}
	return b.String()
}

// What the seeker's record could carry that must never reach the batched prompt,
// each placed where it could leak from: a role's location (FromResolved drops it)
// and the seeker's motivation notes (trusted text that does go to a motivation
// essay, as the second half of the test shows, and does not go to the batch).
// The request type has nowhere to hold a saved answer (TestRequestTypesCarryNoSavedValue).
const (
	secretMail  = "zebra.quuxington@example.test"
	secretPhone = "+1 555 010 0199"
	secretCity  = "Quuxville, ZZ 99999"
)

const notesWithContact = "I care about reliable infrastructure. Reach me at " + secretMail + " or " + secretPhone + "."

func TestNoPersonalDataOrPinnedFieldReachesTheBatchedPrompt(t *testing.T) {
	// The evidence: a role whose location is a secret (dropped by FromResolved) and
	// bullets that are plain work.
	r := kbsync.Resolved{Blocks: []kbsync.Block{{ID: "a", Title: "Backend Engineer", Org: "Example Labs", Start: "2020-02", Location: secretCity,
		Bullets: []kbsync.BlockBullet{{RkbID: "a1", Canonical: "Shipped Python services for 4 years"}}}}}
	el := kbsync.Eligibility{Bullets: map[string]bool{"a1": true}, EntryBullets: map[string][]string{"a": {"a1"}}}
	kb := draft.FromResolved(r, el)

	p := &recProvider{text: `{"q1":{"answer":"4 years","evidence":["e1"]},"q2":{"choice":"LANG-MARK-YES","evidence":["e1"]}}`}
	drafter, err := draft.New(p, draft.Config{Model: "m", TargetSummary: "Backend roles", MotivationNotes: notesWithContact, Timeout: 2 * time.Second}, kb)
	if err != nil {
		t.Fatal(err)
	}

	pinned := []fieldtriage.Field{
		{ID: "p1", Label: "Will you require visa sponsorship? PIN-ONE", Kind: fieldtriage.KindText},
		{ID: "p2", Label: "Are you legally authorized to work in the US? PIN-TWO", Kind: fieldtriage.KindChoice, Options: []string{"OPT-PIN-A", "OPT-PIN-B"}},
		{ID: "p3", Label: "Gender PIN-THREE", Kind: fieldtriage.KindChoice, Options: []string{"OPT-PIN-C", "OPT-PIN-D"}},
		{ID: "p4", Label: "I agree to the arbitration agreement PIN-FOUR", Kind: fieldtriage.KindChoice, Options: []string{"OPT-PIN-E", "OPT-PIN-F"}},
		{ID: "p5", Label: "Date of birth PIN-FIVE", Kind: fieldtriage.KindChoice, Options: []string{"OPT-PIN-G", "OPT-PIN-H"}},
		{ID: "p6", Label: "Veteran status PIN-SIX", Kind: fieldtriage.KindText},
	}
	fields := append([]fieldtriage.Field{
		{ID: "ok1", Label: "How many years have you shipped Python services?", Kind: fieldtriage.KindText},
		// Not "OPT-...": OPT is the F-1 work permit, and an option naming it pins the question (draft.Pinned).
		{ID: "ok2", Label: "Have you shipped Python services?", Kind: fieldtriage.KindChoice, Options: []string{"LANG-MARK-YES", "LANG-MARK-NO"}},
	}, pinned...)
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		"How many years have you shipped Python services?": answered(fieldtriage.Fact),
		"Have you shipped Python services?":                answered(fieldtriage.Fact),
		// The model, were it asked, would call these facts: the engine still pins them.
		"Date of birth PIN-FIVE": answered(fieldtriage.Fact),
	}}
	out, err := Engine{D: d, Drafter: &fakeDrafter{}, Answerer: drafter}.Run(context.Background(), Request{Job: draft.Job{Company: "Modal", Title: "Engineer", Description: strings.Repeat("Build Python services. ", 5)}, Fields: fields})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("%d provider turns, want the one batch", len(p.reqs))
	}
	sent := p.sent()
	for _, s := range []string{secretMail, secretPhone, secretCity, "care about reliable", "PIN-", "OPT-PIN", "visa", "sponsorship", "authorized", "Veteran", "Gender", "arbitration", "birth"} {
		if strings.Contains(sent, s) {
			t.Errorf("the batched prompt contains %q:\n%s", s, sent)
		}
	}
	for _, s := range []string{"LANG-MARK-YES", "LANG-MARK-NO", "Shipped Python services for 4 years"} {
		if !strings.Contains(sent, s) {
			t.Errorf("the batched prompt lacks %q: the test would pass for the wrong reason", s)
		}
	}
	for _, f := range pinned {
		for _, ln := range d.asked {
			for _, o := range f.Options {
				if strings.Contains(ln, o) {
					t.Errorf("option %q of pinned field %s reached the decision model", o, f.ID)
				}
			}
		}
	}
	for _, s := range d.asked {
		if strings.Contains(s, "OPT-") {
			t.Errorf("an option reached the decision model: %q", s)
		}
	}
	// The same drafter does send the notes to a motivation essay: the seam is live,
	// so their absence above is the batch's doing and not an empty fixture.
	essay := &recProvider{text: "I want to build reliable infrastructure."}
	ed, err := draft.New(essay, draft.Config{Model: "m", MotivationNotes: notesWithContact, Timeout: 2 * time.Second}, kb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ed.Draft(context.Background(), draft.Request{Question: "Why Modal?", Job: draft.Job{Company: "Modal", Title: "Engineer", Description: "Build Python services."}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(essay.sent(), secretMail) {
		t.Fatal("the notes did not reach a motivation essay: the test's leak path is not live")
	}
	if strings.Contains(essay.sent(), secretCity) {
		t.Error("a role's location reached an essay prompt")
	}
	for _, s := range out {
		pin := strings.HasPrefix(s.ID, "p")
		if pin && (s.Status != Manual || s.Draft != "" || s.Choice != "" || s.AnswerKind != "") {
			t.Errorf("pinned %s: %+v", s.ID, s)
		}
		if !pin && s.Status != Drafted {
			t.Errorf("%s: %s %s", s.ID, s.Status, s.Reason)
		}
	}
}

// The request has nowhere to hold a saved answer, a name, an email or a phone
// number: a field added to any of these types is a decision, not an accident.
func TestRequestTypesCarryNoSavedValue(t *testing.T) {
	jsonNames := func(v any) []string {
		rt := reflect.TypeOf(v)
		var ns []string
		for i := range rt.NumField() {
			ns = append(ns, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
		}
		return ns
	}
	for _, c := range []struct {
		v    any
		want string
	}{
		{fieldtriage.Field{}, "id,label,kind,max_chars,options"},
		{Request{}, "job,fields,max_drafts"},
		{draft.Job{}, "company,title,description"},
	} {
		if got := strings.Join(jsonNames(c.v), ","); got != c.want {
			t.Errorf("%T carries %s, want %s", c.v, got, c.want)
		}
	}
}
