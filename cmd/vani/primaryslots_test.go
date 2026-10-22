package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// oldTriageModelIDs pins triageModelID for every config that predates
// decide.lappi.primary_slots. A change to any of these discards every cached
// reading, so they are literals.
func TestTriageModelIDOldConfigsAreByteIdentical(t *testing.T) {
	base := config.Default()
	base.TargetSummary = "ML systems"
	// The id is "or=%s;llm=%s/%s;lappi=%s;ctx=%s" and has been since before
	// per-slot routing. The ctx hash moves with the fixture, so the table
	// pins the rest of the string around it.
	cases := []struct {
		name  string
		mut   func(c *config.Config)
		lappi string
	}{
		{"lappi off", func(c *config.Config) {}, "off"},
		{"lappi shadow", func(c *config.Config) { c.Decide.Lappi = &config.Lappi{} }, "shadow"},
		{"lappi primary", func(c *config.Config) { c.Decide.Lappi = &config.Lappi{Primary: true} }, "primary"},
	}
	for _, tc := range cases {
		c := base
		tc.mut(&c)
		got := triageModelID(c)
		pre := "or=" + c.Decide.OpenRouterModel + ";llm=" + c.LLM.Provider + "/" + c.LLM.Model + ";lappi=" + tc.lappi + ";ctx="
		if !strings.HasPrefix(got, pre) || len(got) != len(pre)+12 {
			t.Errorf("%s: id %q, want %q + 12 hex", tc.name, got, pre)
		}
	}
}

func TestTriageModelIDPerSlotIsNewAndDeterministic(t *testing.T) {
	base := config.Default()
	base.TargetSummary = "ML systems"
	slots := func(s ...string) string {
		c := base
		c.Decide.Lappi = &config.Lappi{PrimarySlots: s}
		return triageModelID(c)
	}
	a, b := jdtriage.Families[0].Slot.Name, jdtriage.Families[1].Slot.Name
	if slots(a, b) != slots(b, a) {
		t.Error("the id depends on the order primary_slots is written in")
	}
	if slots(a) == slots(a, b) || slots(a) == slots(b) {
		t.Error("the id does not name the promoted set")
	}
	for name, c := range map[string]*config.Lappi{"shadow": {}, "primary": {Primary: true}} {
		cc := base
		cc.Decide.Lappi = c
		if triageModelID(cc) == slots(a) {
			t.Errorf("a per-slot config shares the %s id", name)
		}
	}
	if !strings.Contains(slots(a, b), "lappi=slots:"+a+"+"+b+";") {
		t.Errorf("id %q does not carry the promoted slots", slots(a, b))
	}
}

// fakeBackend answers every slot it is asked with value, or reports down.
type fakeBackend struct {
	name  string
	value map[string]string // slot -> option; a slot not listed gets the first option
	down  bool
	asked []string
}

func (f *fakeBackend) Name() string       { return f.name }
func (f *fakeBackend) Serves(string) bool { return true }
func (f *fakeBackend) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	out := decide.Answer{Backend: f.name, Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		f.asked = append(f.asked, s.Name)
		if f.down {
			out.Slots[s.Name] = decide.SlotReading{Reading: decide.Unavailable, Detail: "socket_not_found"}
			continue
		}
		v := s.Options[0]
		if x, ok := f.value[s.Name]; ok {
			v = x
		}
		out.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: v}
	}
	return out, nil
}

func perSlotRouter(l, or, llm *fakeBackend, promoted []string) *decide.Router {
	return decide.NewRouter(onlySlots(l, promoted), exceptSlots(or, promoted), llm)
}

func triageQ() decide.Question {
	return jdtriage.Question(posting.Posting{Key: "k", Title: "Engineer", Description: "Fully remote."})
}

func TestPromotedSlotIsAnsweredByLappiOnlyOthersByD1Ultra(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	l := &fakeBackend{name: "lappi"}
	or := &fakeBackend{name: "d1-ultra"}
	llm := &fakeBackend{name: "llm"}
	q := triageQ()
	ra, err := perSlotRouter(l, or, llm, promoted).Route(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range q.Slots {
		want := "d1-ultra"
		if s.Name == promoted[0] {
			want = "lappi"
		}
		if got := ra.SlotBackend[s.Name]; got != want || ra.Slots[s.Name].Reading != decide.Answered {
			t.Errorf("slot %s answered by %q (%v), want %q", s.Name, got, ra.Slots[s.Name].Reading, want)
		}
	}
	if len(l.asked) != 1 || l.asked[0] != promoted[0] {
		t.Errorf("lappi asked %v, want only %v", l.asked, promoted)
	}
	if contains := func() bool {
		for _, a := range or.asked {
			if a == promoted[0] {
				return true
			}
		}
		return false
	}(); contains {
		t.Errorf("d1-ultra was asked the promoted slot: %v", or.asked)
	}
	if len(llm.asked) != 0 {
		t.Errorf("llm asked %v though every slot was answered", llm.asked)
	}
}

// Lappi down for a promoted slot: d1-ultra does not take it back; the next
// chain backend (llm) does, as it would under primary:true, and with no llm the
// slot reads Unavailable, which the pipeline keeps as unsure.
func TestPromotedSlotLappiDownDoesNotFallBackToD1Ultra(t *testing.T) {
	promoted := []string{jdtriage.Families[0].Slot.Name}
	q := triageQ()

	l := &fakeBackend{name: "lappi", down: true}
	or := &fakeBackend{name: "d1-ultra"}
	llm := &fakeBackend{name: "llm"}
	ra, _ := perSlotRouter(l, or, llm, promoted).Route(context.Background(), q)
	if got := ra.SlotBackend[promoted[0]]; got != "llm" {
		t.Errorf("with llm configured the promoted slot went to %q, want llm", got)
	}
	for _, a := range or.asked {
		if a == promoted[0] {
			t.Errorf("d1-ultra asked the promoted slot %s", a)
		}
	}

	l2 := &fakeBackend{name: "lappi", down: true}
	or2 := &fakeBackend{name: "d1-ultra"}
	ra, _ = decide.NewRouter(onlySlots(l2, promoted), exceptSlots(or2, promoted)).Route(context.Background(), q)
	if rd := ra.Slots[promoted[0]]; rd.Reading != decide.Unavailable {
		t.Errorf("promoted slot with Lappi down and no llm reads %v, want Unavailable (unsure)", rd.Reading)
	}

	// The existing primary:true path does fall back to d1-ultra: the
	// documented difference.
	l3 := &fakeBackend{name: "lappi", down: true}
	or3 := &fakeBackend{name: "d1-ultra"}
	ra, _ = decide.NewRouter(l3, or3).Route(context.Background(), q)
	if got := ra.SlotBackend[promoted[0]]; got != "d1-ultra" {
		t.Errorf("primary:true control went to %q", got)
	}
}

func TestSlotFilterAsksNothingWhenNoSlotIsKept(t *testing.T) {
	inner := &fakeBackend{name: "x"}
	a, err := onlySlots(inner, nil).Decide(context.Background(), triageQ())
	if err != nil || len(inner.asked) != 0 {
		t.Fatalf("err %v, inner asked %v", err, inner.asked)
	}
	for n, rd := range a.Slots {
		if rd.Reading != decide.NotAsked {
			t.Errorf("slot %s reads %v", n, rd.Reading)
		}
	}
}

// The wiring: promoted slots put a Lappi chain instance first and leave a
// Lappi shadow on the other slots; the shadow is the second client.
func TestPrimarySlotsWiring(t *testing.T) {
	clearKeys(t)
	t.Setenv("GEMINI_API_KEY", "test-not-a-real-key")
	cfg := defaultWithLLM()
	cfg.Decide.OpenRouterModel = ""
	promoted := []string{jdtriage.Families[0].Slot.Name}
	cfg.Decide.Lappi = &config.Lappi{Socket: "/tmp/vani-test-no-agent.sock", PrimarySlots: promoted}
	cfg.Decide.Shadow = "lappi"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	w := &wired{cfg: cfg, stateDir: t.TempDir(), warn: &bytes.Buffer{}}
	r, err := w.runner(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := r.Triager.(*jdtriage.Triager).D.(*decide.Router)
	if got := strings.Join(rt.Backends(), ","); got != "lappi,llm" || rt.Shadow == nil || rt.Shadow.Name() != "lappi" {
		t.Fatalf("chain %q shadow %v", got, rt.Shadow)
	}
	// Both are filtered: the chain instance reads only promoted slots (and
	// finds no agent), the shadow reads the rest and reports the others not asked.
	a, err := rt.Shadow.Decide(context.Background(), triageQ())
	if err != nil {
		t.Fatal(err)
	}
	if rd := a.Slots[promoted[0]]; rd.Reading != decide.NotAsked {
		t.Errorf("shadow read the promoted slot: %+v", rd)
	}
}
