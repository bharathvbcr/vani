package decide

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	name   string
	serves bool
	fn     func(ctx context.Context, q Question) (Answer, error)

	mu    sync.Mutex
	asked [][]string // slot names per call
}

func (f *fakeBackend) Name() string       { return f.name }
func (f *fakeBackend) Serves(string) bool { return f.serves }
func (f *fakeBackend) callCount() int     { f.mu.Lock(); defer f.mu.Unlock(); return len(f.asked) }
func (f *fakeBackend) Decide(ctx context.Context, q Question) (Answer, error) {
	names := make([]string, len(q.Slots))
	for i, s := range q.Slots {
		names[i] = s.Name
	}
	f.mu.Lock()
	f.asked = append(f.asked, names)
	f.mu.Unlock()
	return f.fn(ctx, q)
}

// answering returns a backend that answers the slots in vals with the given
// option and reports every other slot as abstained.
func answering(name string, vals map[string]string) *fakeBackend {
	return &fakeBackend{name: name, serves: true, fn: func(_ context.Context, q Question) (Answer, error) {
		a := Answer{Backend: name, Slots: map[string]SlotReading{}, CostUSD: 0.5}
		for _, s := range q.Slots {
			if v, ok := vals[s.Name]; ok {
				a.Slots[s.Name] = SlotReading{Reading: Answered, Value: v}
			} else {
				a.Slots[s.Name] = SlotReading{Reading: Abstained}
			}
		}
		return a, nil
	}}
}

func twoSlots() Question {
	return Question{Task: "t", Prompt: "p", Slots: []Slot{
		{Name: "a", Options: []string{"a1", "a2"}},
		{Name: "b", Options: []string{"b1", "b2"}},
	}}
}

func TestRouterFallsThroughPerSlot(t *testing.T) {
	first := answering("first", map[string]string{"a": "a1"})
	second := answering("second", map[string]string{"b": "b2"})
	third := answering("third", map[string]string{"a": "a2", "b": "b1"})
	r := NewRouter(first, second, third)
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil {
		t.Fatal(err)
	}
	if got := ra.Slots["a"]; got.Reading != Answered || got.Value != "a1" || got.Detail != "backend=first" {
		t.Errorf("a = %+v", got)
	}
	if got := ra.Slots["b"]; got.Value != "b2" || got.Detail != "backend=second" {
		t.Errorf("b = %+v", got)
	}
	if third.callCount() != 0 {
		t.Error("third asked although everything was answered")
	}
	if fmt.Sprint(second.asked) != "[[b]]" {
		t.Errorf("second was asked %v, want only the remaining slot b", second.asked)
	}
	if ra.SlotBackend["a"] != "first" || ra.SlotBackend["b"] != "second" {
		t.Errorf("SlotBackend = %v", ra.SlotBackend)
	}
	if ra.CostUSD != 1.0 {
		t.Errorf("cost = %v, want 1.0", ra.CostUSD)
	}
	if ra.Backend != "first+second" {
		t.Errorf("Backend = %q", ra.Backend)
	}
}

func TestRouterKeepsUnansweredReadings(t *testing.T) {
	r := NewRouter(answering("only", nil))
	a, err := r.Decide(context.Background(), twoSlots())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if got := a.Slots[name]; got.Reading != Abstained || got.Value != "" || got.Detail != "backend=only" {
			t.Errorf("%s = %+v", name, got)
		}
	}
	if a.Backend != "router" {
		t.Errorf("Backend = %q", a.Backend)
	}
}

func TestRouterSkipsUnservedBackend(t *testing.T) {
	skipped := answering("skipped", map[string]string{"a": "a1", "b": "b1"})
	skipped.serves = false
	next := answering("next", map[string]string{"a": "a2", "b": "b2"})
	ra, err := NewRouter(skipped, next).Route(context.Background(), twoSlots())
	if err != nil {
		t.Fatal(err)
	}
	if skipped.callCount() != 0 {
		t.Error("unserved backend was called")
	}
	if ra.Slots["a"].Value != "a2" {
		t.Errorf("a = %+v", ra.Slots["a"])
	}

	// With nothing else to ask, the skip is recorded as NotAsked.
	only := answering("only", nil)
	only.serves = false
	ra, _ = NewRouter(only).Route(context.Background(), twoSlots())
	if got := ra.Slots["a"]; got.Reading != NotAsked || got.Detail != "backend=only" {
		t.Errorf("a = %+v", got)
	}
}

func TestRouterRejectsValueOutsideOptions(t *testing.T) {
	liar := answering("liar", map[string]string{"a": "not-an-option", "b": ""})
	honest := answering("honest", map[string]string{"a": "a2", "b": "b1"})
	ra, _ := NewRouter(liar, honest).Route(context.Background(), twoSlots())
	if ra.Slots["a"].Value != "a2" || ra.SlotBackend["a"] != "honest" {
		t.Errorf("a = %+v from %q", ra.Slots["a"], ra.SlotBackend["a"])
	}
	// With no honest fallback the bad claim becomes Failed, never a value.
	ra, _ = NewRouter(liar).Route(context.Background(), twoSlots())
	for _, n := range []string{"a", "b"} {
		if got := ra.Slots[n]; got.Reading != Failed || got.Value != "" {
			t.Errorf("%s = %+v", n, got)
		}
	}
}

func TestRouterMissingSlotInAnswerIsFailed(t *testing.T) {
	b := &fakeBackend{name: "b", serves: true, fn: func(context.Context, Question) (Answer, error) {
		return Answer{Slots: map[string]SlotReading{"a": {Reading: Answered, Value: "a1"}}}, nil
	}}
	ra, _ := NewRouter(b).Route(context.Background(), twoSlots())
	if ra.Slots["a"].Reading != Answered || ra.Slots["b"].Reading != Failed {
		t.Errorf("slots = %+v", ra.Slots)
	}
}

func TestRouterBackendErrorFallsThroughAndAuthDisables(t *testing.T) {
	bad := &fakeBackend{name: "bad", serves: true, fn: func(context.Context, Question) (Answer, error) {
		return Answer{CostUSD: 0.25}, fmt.Errorf("wrap: %w", ErrAuth)
	}}
	good := answering("good", map[string]string{"a": "a1", "b": "b1"})
	r := NewRouter(bad, good)
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil || ra.Slots["a"].Value != "a1" {
		t.Fatalf("err=%v slots=%+v", err, ra.Slots)
	}
	if ra.CostUSD != 0.75 {
		t.Errorf("cost = %v, want error-path cost summed", ra.CostUSD)
	}
	if _, err := r.Route(context.Background(), twoSlots()); err != nil {
		t.Fatal(err)
	}
	if bad.callCount() != 1 {
		t.Errorf("auth-failed backend asked %d times, want 1", bad.callCount())
	}

	// Alone, the error reads Unavailable with a class, not the error text.
	alone := NewRouter(&fakeBackend{name: "x", serves: true, fn: func(context.Context, Question) (Answer, error) {
		return Answer{}, errors.New("secret-token-123 exploded")
	}})
	ra, _ = alone.Route(context.Background(), twoSlots())
	got := ra.Slots["a"]
	if got.Reading != Unavailable || strings.Contains(got.Detail, "secret") {
		t.Errorf("a = %+v", got)
	}
}

func TestRouterContainsBackendPanic(t *testing.T) {
	p := &fakeBackend{name: "p", serves: true, fn: func(context.Context, Question) (Answer, error) { panic("boom") }}
	good := answering("good", map[string]string{"a": "a1", "b": "b1"})
	ra, err := NewRouter(p, good).Route(context.Background(), twoSlots())
	if err != nil || ra.Slots["a"].Value != "a1" {
		t.Fatalf("err=%v slots=%+v", err, ra.Slots)
	}
}

func TestRouterValidatesFirst(t *testing.T) {
	b := answering("b", nil)
	q := twoSlots()
	q.Slots[0].Options = []string{"only"}
	_, err := NewRouter(b).Decide(context.Background(), q)
	if !errors.Is(err, ErrInvalidQuestion) || b.callCount() != 0 {
		t.Fatalf("err=%v calls=%d", err, b.callCount())
	}
}

func TestRouterBoundedByContext(t *testing.T) {
	slow := &fakeBackend{name: "slow", serves: true, fn: func(ctx context.Context, q Question) (Answer, error) {
		<-ctx.Done()
		return Answer{}, ctx.Err()
	}}
	late := answering("late", map[string]string{"a": "a1", "b": "b1"})
	r := NewRouter(slow, late)
	r.Timeout = 20 * time.Millisecond
	start := time.Now()
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("router outlived its timeout")
	}
	if late.callCount() != 0 {
		t.Error("backend asked after the deadline")
	}
	if got := ra.Slots["a"]; got.Reading != Unavailable {
		t.Errorf("a = %+v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ra, _ = NewRouter(late).Route(ctx, twoSlots())
	if got := ra.Slots["a"]; got.Reading != Unavailable || got.Detail != "deadline" {
		t.Errorf("pre-cancelled: a = %+v", got)
	}
}

func TestShadowIsNeverAuthoritativeAndDisagreementFires(t *testing.T) {
	primary := answering("primary", map[string]string{"a": "a1"}) // b abstained
	shadow := answering("shadow", map[string]string{"a": "a2", "b": "b2"})
	type dis struct {
		task, slot string
		p, s       SlotReading
	}
	var got []dis
	r := NewRouter(primary)
	r.Shadow = shadow
	r.OnDisagree = func(task, slot string, p, s SlotReading) { got = append(got, dis{task, slot, p, s}) }
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil {
		t.Fatal(err)
	}
	if ra.Slots["a"].Value != "a1" || ra.Slots["b"].Reading != Abstained {
		t.Errorf("shadow leaked into result: %+v", ra.Slots)
	}
	if ra.CostUSD != 0.5 {
		t.Errorf("shadow cost in result: %v", ra.CostUSD)
	}
	if ra.ShadowCostUSD != 0.5 {
		t.Errorf("ShadowCostUSD = %v", ra.ShadowCostUSD)
	}
	if len(got) != 2 {
		t.Fatalf("disagreements = %d, want 2: %+v", len(got), got)
	}
	for _, d := range got {
		if d.task != "t" {
			t.Errorf("task = %q", d.task)
		}
	}
}

func TestShadowReadingsAreReturnedSeparately(t *testing.T) {
	r := NewRouter(answering("primary", map[string]string{"a": "a1"}))
	r.Shadow = answering("shadow", map[string]string{"a": "a2", "b": "zzz"}) // "zzz" is not an option of b
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil {
		t.Fatal(err)
	}
	if got := ra.Shadow["a"]; got.Detail != "backend=shadow" {
		t.Errorf("shadow reading not tagged with its backend: %q", got.Detail)
	}
	if got := ra.Shadow["a"]; got.Reading != Answered || got.Value != "a2" {
		t.Errorf("shadow a = %+v", got)
	}
	if got := ra.Shadow["b"]; got.Reading != Failed || got.Value != "" {
		t.Errorf("invalid shadow value not sanitized: %+v", got)
	}
	if ra.Slots["a"].Value != "a1" {
		t.Errorf("shadow leaked into Slots: %+v", ra.Slots)
	}
	// Decide returns the same Answer, Shadow included.
	ans, err := r.Decide(context.Background(), twoSlots())
	if err != nil || ans.Shadow["a"].Value != "a2" {
		t.Errorf("Decide dropped Shadow: %v %+v", err, ans.Shadow)
	}
	// No shadow configured: nothing recorded. A shadow that errors is recorded as
	// unavailable on every slot (it was asked), never as missing.
	r2 := NewRouter(answering("primary", map[string]string{"a": "a1"}))
	if ra, _ := r2.Route(context.Background(), twoSlots()); ra.Shadow != nil {
		t.Errorf("Shadow without a shadow backend: %+v", ra.Shadow)
	}
	r2.Shadow = &fakeBackend{name: "broken", serves: true, fn: func(context.Context, Question) (Answer, error) {
		return Answer{}, errors.New("down")
	}}
	ra, _ = r2.Route(context.Background(), twoSlots())
	for _, slot := range []string{"a", "b"} {
		if got := ra.Shadow[slot]; got.Reading != Unavailable || got.Detail != "error;backend=broken" || got.Value != "" {
			t.Errorf("Shadow[%s] after shadow error: %+v", slot, got)
		}
	}
	if len(ra.Shadow) != 2 {
		t.Errorf("Shadow after shadow error has %d slots: %+v", len(ra.Shadow), ra.Shadow)
	}
}

// A shadow that fails or hangs is recorded as unavailable on every slot, tagged
// with its class and backend, without changing the result; its outage is not a
// disagreement and is counted once per failed call.
func TestFailedShadowIsRecordedUnavailable(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(context.Context, Question) (Answer, error)
		timeout time.Duration
		detail  string
	}{
		{"error", func(context.Context, Question) (Answer, error) { return Answer{CostUSD: 0.25}, errors.New("down") }, 0, "error"},
		{"auth", func(context.Context, Question) (Answer, error) { return Answer{}, ErrAuth }, 0, "auth"},
		{"panic", func(context.Context, Question) (Answer, error) { panic("boom") }, 0, "error"},
		{"timeout", func(ctx context.Context, _ Question) (Answer, error) { <-ctx.Done(); return Answer{}, ctx.Err() }, 20 * time.Millisecond, "deadline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fired := 0
			r := NewRouter(answering("primary", map[string]string{"a": "a1", "b": "b1"}))
			r.Timeout = tc.timeout
			r.Shadow = &fakeBackend{name: "shd", serves: true, fn: tc.fn}
			r.OnDisagree = func(string, string, SlotReading, SlotReading) { fired++ }
			ra, err := r.Route(context.Background(), twoSlots())
			if err != nil || ra.Slots["a"].Value != "a1" || ra.Slots["b"].Value != "b1" {
				t.Fatalf("shadow failure changed the result: %v %+v", err, ra.Slots)
			}
			if len(ra.Shadow) != 2 {
				t.Fatalf("Shadow = %+v, want both slots", ra.Shadow)
			}
			for _, slot := range []string{"a", "b"} {
				want := SlotReading{Reading: Unavailable, Detail: tc.detail + ";backend=shd"}
				if got := ra.Shadow[slot]; got != want {
					t.Errorf("Shadow[%s] = %+v, want %+v", slot, got, want)
				}
			}
			if fired != 0 {
				t.Errorf("OnDisagree fired %d times for an outage", fired)
			}
			if r.ShadowErrors() != 1 {
				t.Errorf("ShadowErrors = %d, want 1 (one failed call)", r.ShadowErrors())
			}
		})
	}
}

// OnDisagree is for shadows that read the slot: an abstaining shadow against an
// answering primary still fires; a refused or failed shadow reading does not.
func TestOnDisagreeNeedsAShadowThatRead(t *testing.T) {
	mk := func(rd SlotReading) *Router {
		r := NewRouter(answering("primary", map[string]string{"a": "a1", "b": "b1"}))
		r.Shadow = &fakeBackend{name: "shd", serves: true, fn: func(_ context.Context, q Question) (Answer, error) {
			a := Answer{Backend: "shd", Slots: map[string]SlotReading{}}
			for _, s := range q.Slots {
				a.Slots[s.Name] = rd
			}
			return a, nil
		}}
		return r
	}
	for _, tc := range []struct {
		name string
		rd   SlotReading
		want int
	}{
		{"abstained", SlotReading{Reading: Abstained}, 2},
		// "a2" is an option of a (a differing reading) but not of b (sanitized to Failed, silent).
		{"answered differently", SlotReading{Reading: Answered, Value: "a2"}, 1},
		{"refused", SlotReading{Reading: Refused}, 0},
		{"failed", SlotReading{Reading: Failed}, 0},
		{"unavailable", SlotReading{Reading: Unavailable}, 0},
	} {
		fired := 0
		r := mk(tc.rd)
		r.OnDisagree = func(string, string, SlotReading, SlotReading) { fired++ }
		if _, err := r.Route(context.Background(), twoSlots()); err != nil {
			t.Fatal(err)
		}
		if fired != tc.want {
			t.Errorf("%s: OnDisagree fired %d times, want %d", tc.name, fired, tc.want)
		}
	}
}

func TestShadowAgreementIsSilentAndErrorsAreSwallowedAndCounted(t *testing.T) {
	primary := answering("primary", map[string]string{"a": "a1", "b": "b1"})
	fired := 0
	r := NewRouter(primary)
	r.Shadow = answering("same", map[string]string{"a": "a1", "b": "b1"})
	r.OnDisagree = func(string, string, SlotReading, SlotReading) { fired++ }
	if _, err := r.Route(context.Background(), twoSlots()); err != nil || fired != 0 {
		t.Fatalf("err=%v fired=%d", err, fired)
	}

	r.Shadow = &fakeBackend{name: "broken", serves: true, fn: func(context.Context, Question) (Answer, error) {
		return Answer{}, errors.New("down")
	}}
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil || ra.Slots["a"].Value != "a1" {
		t.Fatalf("shadow error changed the result: %v %+v", err, ra.Slots)
	}
	if r.ShadowErrors() != 1 || fired != 0 {
		t.Errorf("ShadowErrors=%d fired=%d", r.ShadowErrors(), fired)
	}

	r.Shadow = answering("same", map[string]string{"a": "a1", "b": "b1"})
	r.OnDisagree = func(string, string, SlotReading, SlotReading) { panic("callback bug") }
	r.Shadow = answering("diff", map[string]string{"a": "a2", "b": "b2"})
	ra, err = r.Route(context.Background(), twoSlots())
	if err != nil || ra.Slots["a"].Value != "a1" {
		t.Fatalf("callback panic changed the result: %v %+v", err, ra.Slots)
	}
}

func TestBackendsListsTheChainInOrder(t *testing.T) {
	r := NewRouter(answering("lappi", nil), answering("jev", nil))
	r.Shadow = answering("llm", nil)
	if got := strings.Join(r.Backends(), ","); got != "lappi,jev" {
		t.Fatalf("Backends() = %q", got)
	}
	if len(NewRouter().Backends()) != 0 {
		t.Fatal("empty router lists backends")
	}
}
