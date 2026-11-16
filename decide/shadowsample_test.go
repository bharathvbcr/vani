package decide

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// An unsampled question leaves the shadow unasked and the answer as if no
// shadow were configured: no readings, no cost, no error, no OnDisagree.
func TestShadowSampleSkipsUnsampledQuestions(t *testing.T) {
	shadow := answering("shd", map[string]string{"a": "a2", "b": "b2"})
	r := NewRouter(answering("primary", map[string]string{"a": "a1", "b": "b1"}))
	r.Shadow = shadow
	fired := 0
	r.OnDisagree = func(string, string, SlotReading, SlotReading) { fired++ }
	var seen []string
	r.ShadowSample = func(q Question) bool {
		seen = append(seen, q.RecordID)
		return q.RecordID == "yes"
	}

	q := twoSlots()
	q.RecordID = "no"
	ra, err := r.Route(context.Background(), q)
	if err != nil || ra.Slots["a"].Value != "a1" || ra.Shadow != nil || ra.ShadowCostUSD != 0 || shadow.callCount() != 0 || fired != 0 {
		t.Fatalf("unsampled: err %v answer %+v calls %d fired %d", err, ra, shadow.callCount(), fired)
	}

	q.RecordID = "yes"
	ra, err = r.Route(context.Background(), q)
	if err != nil || ra.Slots["a"].Value != "a1" || len(ra.Shadow) != 2 || shadow.callCount() != 1 || fired != 2 {
		t.Fatalf("sampled: err %v answer %+v calls %d fired %d", err, ra, shadow.callCount(), fired)
	}
	if len(seen) != 2 || seen[0] != "no" || seen[1] != "yes" || r.ShadowErrors() != 0 {
		t.Fatalf("sample saw %v, shadow errors %d", seen, r.ShadowErrors())
	}
}

// ShadowSample is asked only when the shadow serves the task, and a true
// return is followed by exactly one shadow call however the call goes, so a
// slot reserved in ShadowSample is always released.
func TestShadowSampleTrueMeansExactlyOneCall(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() context.Context
		fn   func(context.Context, Question) (Answer, error)
	}{
		{"answers", context.Background, func(context.Context, Question) (Answer, error) { return Answer{}, nil }},
		{"errors", context.Background, func(context.Context, Question) (Answer, error) { return Answer{}, errors.New("down") }},
		{"panics", context.Background, func(context.Context, Question) (Answer, error) { panic("boom") }},
		{"cancelled", func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, func(ctx context.Context, _ Question) (Answer, error) { return Answer{}, ctx.Err() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reserved atomic.Int64
			released := make(chan struct{}, 4)
			shadow := &fakeBackend{name: "shd", serves: true, fn: func(ctx context.Context, q Question) (Answer, error) {
				defer func() { released <- struct{}{} }()
				return tc.fn(ctx, q)
			}}
			r := NewRouter(answering("primary", map[string]string{"a": "a1", "b": "b1"}))
			r.Shadow = shadow
			r.ShadowSample = func(Question) bool { reserved.Add(1); return true }
			if _, err := r.Route(tc.ctx(), twoSlots()); err != nil {
				t.Fatal(err)
			}
			// Route may return on ctx before the shadow goroutine finishes.
			select {
			case <-released:
			case <-time.After(5 * time.Second):
				t.Fatal("a sampled question never reached the shadow")
			}
			select {
			case <-released:
				t.Fatal("one sample, two shadow calls")
			case <-time.After(20 * time.Millisecond):
			}
			if reserved.Load() != 1 || shadow.callCount() != 1 {
				t.Fatalf("reserved %d, shadow calls %d", reserved.Load(), shadow.callCount())
			}
		})
	}

	t.Run("not asked when the shadow does not serve the task", func(t *testing.T) {
		r := NewRouter(answering("primary", map[string]string{"a": "a1", "b": "b1"}))
		r.Shadow = &fakeBackend{name: "shd", serves: false}
		r.ShadowSample = func(Question) bool { t.Fatal("sampled for a shadow that does not serve the task"); return true }
		if _, err := r.Route(context.Background(), twoSlots()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("not asked for an invalid question", func(t *testing.T) {
		r := NewRouter(answering("primary", nil))
		r.Shadow = answering("shd", nil)
		r.ShadowSample = func(Question) bool { t.Fatal("sampled an invalid question"); return true }
		if _, err := r.Route(context.Background(), Question{}); err == nil {
			t.Fatal("invalid question accepted")
		}
	})
}

// A panicking ShadowSample is contained: the shadow is not asked, the result
// stands, and the panic is counted as a shadow error.
func TestShadowSamplePanicIsContained(t *testing.T) {
	shadow := answering("shd", map[string]string{"a": "a1", "b": "b1"})
	r := NewRouter(answering("primary", map[string]string{"a": "a1", "b": "b1"}))
	r.Shadow = shadow
	r.ShadowSample = func(Question) bool { panic("boom") }
	ra, err := r.Route(context.Background(), twoSlots())
	if err != nil || ra.Slots["a"].Value != "a1" || ra.Shadow != nil || shadow.callCount() != 0 || r.ShadowErrors() != 1 {
		t.Fatalf("err %v answer %+v calls %d shadow errors %d", err, ra, shadow.callCount(), r.ShadowErrors())
	}
}
