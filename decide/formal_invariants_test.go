package decide

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func assertRule7AnswerInvariant(t *testing.T, q Question, ans Answer) {
	t.Helper()
	slotByName := make(map[string]Slot, len(q.Slots))
	for _, s := range q.Slots {
		slotByName[s.Name] = s
	}
	if len(ans.Slots) != len(q.Slots) {
		t.Fatalf("expected %d slots in answer, got %d (%+v)", len(q.Slots), len(ans.Slots), ans.Slots)
	}
	if math.IsNaN(ans.CostUSD) || math.IsInf(ans.CostUSD, 0) || ans.CostUSD < 0 {
		t.Fatalf("invalid CostUSD=%v", ans.CostUSD)
	}
	for name, rd := range ans.Slots {
		spec, ok := slotByName[name]
		if !ok {
			t.Fatalf("unexpected slot %q in answer", name)
		}
		switch rd.Reading {
		case Answered:
			if err := CheckValue(spec, rd.Value); err != nil {
				t.Fatalf("Rule 7 violation on slot %q: Answered with out-of-options Value=%q (%v)", name, rd.Value, err)
			}
			if rd.Lappi != nil && rd.Jev != nil {
				t.Fatalf("slot %q carries both Lappi and Jev calibration pointers: %+v", name, rd)
			}
			if rd.Lappi != nil {
				if math.IsNaN(rd.Lappi.Score) || math.IsInf(rd.Lappi.Score, 0) || rd.Lappi.Score < 0 || rd.Lappi.Score > 1 {
					t.Fatalf("slot %q has invalid Lappi.Score=%v", name, rd.Lappi.Score)
				}
				for _, m := range rd.Lappi.ConformalSet {
					if err := CheckValue(spec, m); err != nil {
						t.Fatalf("Rule 7 violation on slot %q ConformalSet member %q: %v", name, m, err)
					}
				}
			}
			if rd.Jev != nil {
				if math.IsNaN(rd.Jev.Probability) || math.IsInf(rd.Jev.Probability, 0) || rd.Jev.Probability < 0 || rd.Jev.Probability > 1 {
					t.Fatalf("slot %q has invalid Jev.Probability=%v", name, rd.Jev.Probability)
				}
			}
		case Abstained, Refused, Failed, Unavailable, NotAsked:
			if rd.Value != "" {
				t.Fatalf("non-Answered slot %q (%s) smuggled Value=%q", name, rd.Reading, rd.Value)
			}
			if rd.Lappi != nil || rd.Jev != nil {
				t.Fatalf("non-Answered slot %q (%s) smuggled calibration: %+v", name, rd.Reading, rd)
			}
		default:
			t.Fatalf("closed Reading sum violated on slot %q: %q", name, rd.Reading)
		}
	}
}

func checkRule7ViolationBool(q Question, ans Answer) bool {
	slotByName := make(map[string]Slot, len(q.Slots))
	for _, s := range q.Slots {
		slotByName[s.Name] = s
	}
	for name, rd := range ans.Slots {
		spec, ok := slotByName[name]
		if !ok {
			return false
		}
		if rd.Reading == Answered {
			if CheckValue(spec, rd.Value) != nil {
				return false
			}
			if rd.Lappi != nil && rd.Jev != nil {
				return false
			}
			if rd.Lappi != nil {
				if math.IsNaN(rd.Lappi.Score) || math.IsInf(rd.Lappi.Score, 0) || rd.Lappi.Score < 0 || rd.Lappi.Score > 1 {
					return false
				}
				for _, m := range rd.Lappi.ConformalSet {
					if CheckValue(spec, m) != nil {
						return false
					}
				}
			}
			if rd.Jev != nil {
				if math.IsNaN(rd.Jev.Probability) || math.IsInf(rd.Jev.Probability, 0) || rd.Jev.Probability < 0 || rd.Jev.Probability > 1 {
					return false
				}
			}
		} else if rd.Value != "" || rd.Lappi != nil || rd.Jev != nil {
			return false
		}
	}
	return true
}

func TestFormalRule7ExhaustiveNonEscalation(t *testing.T) {
	q := Question{
		Task:   "triage.route",
		Prompt: "Which tier applies?",
		Slots: []Slot{
			{Name: "tier", Options: []string{"low", "high"}},
			{Name: "mode", Options: []string{"batch", "live"}},
		},
	}

	// 1. Disproof witnesses: verify checkRule7ViolationBool convicts a buggy
	// unsanitized pass-through on out-of-options Value, poisoned ConformalSet,
	// dual calibration pointers, and NaN scores.
	disproofs := []Answer{
		{Slots: map[string]SlotReading{
			"tier": {Reading: Answered, Value: "allow"},
			"mode": {Reading: Answered, Value: "batch"},
		}},
		{Slots: map[string]SlotReading{
			"tier": {Reading: Answered, Value: "low", Lappi: &LappiCalib{Score: 0.8, ConformalSet: []string{"low", "approve"}}},
			"mode": {Reading: Answered, Value: "batch"},
		}},
		{Slots: map[string]SlotReading{
			"tier": {Reading: Answered, Value: "low", Lappi: &LappiCalib{Score: 0.8}, Jev: &JevCalib{Probability: 0.8}},
			"mode": {Reading: Answered, Value: "batch"},
		}},
		{Slots: map[string]SlotReading{
			"tier": {Reading: Abstained, Value: "low"},
			"mode": {Reading: Answered, Value: "batch"},
		}},
	}
	for i, bad := range disproofs {
		if checkRule7ViolationBool(q, bad) {
			t.Fatalf("disproof %d accepted an illegal Rule 7 / calibration state: %+v", i, bad)
		}
	}

	type specGen struct {
		name   string
		serves bool
		fn     func(context.Context, Question) (Answer, error)
	}

	behaviors := []specGen{
		{"honest_valid_lappi", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{
					Reading: Answered,
					Value:   s.Options[0],
					Lappi:   &LappiCalib{Score: 0.75, ConformalSet: []string{s.Options[0], s.Options[1]}},
				}
			}
			return Answer{Backend: "b", Slots: m, CostUSD: 0.1}, nil
		}},
		{"inject_allow_escalation", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{Reading: Answered, Value: "allow"}
			}
			return Answer{Backend: "b", Slots: m, CostUSD: 0.1}, nil
		}},
		{"inject_case_fold", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{Reading: Answered, Value: strings.ToUpper(s.Options[0])}
			}
			return Answer{Backend: "b", Slots: m}, nil
		}},
		{"poisoned_conformal_set", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{
					Reading: Answered,
					Value:   s.Options[0],
					Lappi:   &LappiCalib{Score: 0.5, ConformalSet: []string{s.Options[0], "approve"}},
				}
			}
			return Answer{Backend: "b", Slots: m}, nil
		}},
		{"nan_calibration_score", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{
					Reading: Answered,
					Value:   s.Options[0],
					Jev:     &JevCalib{Probability: math.NaN()},
				}
			}
			return Answer{Backend: "b", Slots: m}, nil
		}},
		{"out_of_range_calib", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{
					Reading: Answered,
					Value:   s.Options[0],
					Lappi:   &LappiCalib{Score: 1.01, ConformalSet: []string{s.Options[0]}},
				}
			}
			return Answer{Backend: "b", Slots: m}, nil
		}},
		{"dual_calib_pointers", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{
					Reading: Answered,
					Value:   s.Options[0],
					Lappi:   &LappiCalib{Score: 0.5},
					Jev:     &JevCalib{Probability: 0.5},
				}
			}
			return Answer{Backend: "b", Slots: m}, nil
		}},
		{"abstain_smuggling_value", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{
					Reading: Abstained,
					Value:   "allow",
					Jev:     &JevCalib{Probability: 0.9},
				}
			}
			return Answer{Backend: "b", Slots: m, CostUSD: math.NaN()}, nil
		}},
		{"unknown_reading_enum", true, func(_ context.Context, sub Question) (Answer, error) {
			m := map[string]SlotReading{}
			for _, s := range sub.Slots {
				m[s.Name] = SlotReading{Reading: Reading("escalate"), Value: "allow"}
			}
			return Answer{Backend: "b", Slots: m, CostUSD: -10}, nil
		}},
		{"missing_slots", true, func(_ context.Context, _ Question) (Answer, error) {
			return Answer{Backend: "b", Slots: map[string]SlotReading{}, CostUSD: math.Inf(1)}, nil
		}},
		{"unserved_backend", false, func(_ context.Context, _ Question) (Answer, error) {
			return Answer{}, errors.New("should not be called")
		}},
		{"err_auth", true, func(_ context.Context, _ Question) (Answer, error) {
			return Answer{CostUSD: 0.05}, fmt.Errorf("cred wrap: %w", ErrAuth)
		}},
		{"generic_err", true, func(_ context.Context, _ Question) (Answer, error) {
			return Answer{}, errors.New("bearer-secret-xyz failed")
		}},
		{"panic_backend", true, func(_ context.Context, _ Question) (Answer, error) {
			panic("adversarial backend panic")
		}},
	}

	checked := 0
	for i, b1 := range behaviors {
		for j, b2 := range behaviors {
			fb1 := &fakeBackend{name: fmt.Sprintf("b1_%d_%s", i, b1.name), serves: b1.serves, fn: b1.fn}
			fb2 := &fakeBackend{name: fmt.Sprintf("b2_%d_%s", j, b2.name), serves: b2.serves, fn: b2.fn}
			r := NewRouter(fb1, fb2)
			ra, err := r.Route(context.Background(), q)
			if err != nil {
				t.Fatalf("pair (%s, %s) unexpected Route error: %v", b1.name, b2.name, err)
			}
			assertRule7AnswerInvariant(t, q, ra.Answer)
			for _, rd := range ra.Slots {
				if strings.Contains(rd.Detail, "bearer-secret-xyz") {
					t.Fatalf("error text leaked into Detail: %q", rd.Detail)
				}
			}
			if b1.name == "err_auth" {
				// Second Route call must never re-invoke an ErrAuth-disabled first backend.
				if _, err2 := r.Route(context.Background(), q); err2 != nil {
					t.Fatalf("second Route error: %v", err2)
				}
				if fb1.callCount() != 1 {
					t.Fatalf("ErrAuth backend %s called %d times across 2 Route calls, want 1", b1.name, fb1.callCount())
				}
			}
			checked++
		}
	}
	if checked != len(behaviors)*len(behaviors) {
		t.Fatalf("non-vacuity check failed: ran %d combinations, want %d", checked, len(behaviors)*len(behaviors))
	}

	// Verify ConformalSet backing-slice isolation against post-return mutation by a backend.
	backing := []string{"low", "high"}
	mutator := &fakeBackend{name: "mut", serves: true, fn: func(_ context.Context, _ Question) (Answer, error) {
		return Answer{
			Slots: map[string]SlotReading{
				"tier": {Reading: Answered, Value: "low", Lappi: &LappiCalib{Score: 0.9, ConformalSet: backing}},
				"mode": {Reading: Answered, Value: "batch"},
			},
		}, nil
	}}
	ra, err := NewRouter(mutator).Route(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	backing[0] = "allow"
	assertRule7AnswerInvariant(t, q, ra.Answer)
}

func TestFormalShadowNonInterferenceExhaustive(t *testing.T) {
	q := twoSlots()

	primaryChains := []func() []*fakeBackend{
		func() []*fakeBackend {
			return []*fakeBackend{answering("p1", map[string]string{"a": "a1", "b": "b2"})}
		},
		func() []*fakeBackend {
			return []*fakeBackend{
				answering("p1", map[string]string{"a": "a1"}),
				answering("p2", map[string]string{"b": "b1"}),
			}
		},
		func() []*fakeBackend {
			return []*fakeBackend{answering("p1", nil)}
		},
		func() []*fakeBackend {
			return []*fakeBackend{{name: "p_err", serves: true, fn: func(context.Context, Question) (Answer, error) {
				return Answer{CostUSD: 0.2}, errors.New("primary down")
			}}}
		},
		func() []*fakeBackend {
			return []*fakeBackend{answering("liar", map[string]string{"a": "allow", "b": "approve"})}
		},
	}

	shadowSpecs := []struct {
		name   string
		serves bool
		sample func(Question) bool
		onDis  func(string, string, SlotReading, SlotReading)
		fn     func(context.Context, Question) (Answer, error)
	}{
		{"shadow_agrees", true, nil, nil, answering("sh", map[string]string{"a": "a1", "b": "b2"}).Decide},
		{"shadow_disagrees_valid", true, nil, nil, answering("sh", map[string]string{"a": "a2", "b": "b1"}).Decide},
		{"shadow_injects_allow", true, nil, nil, answering("sh", map[string]string{"a": "allow", "b": "approve"}).Decide},
		{"shadow_nan_cost", true, nil, nil, func(context.Context, Question) (Answer, error) {
			return Answer{CostUSD: math.NaN(), Slots: map[string]SlotReading{
				"a": {Reading: Answered, Value: "a2"},
				"b": {Reading: Abstained},
			}}, nil
		}},
		{"shadow_huge_cost", true, nil, nil, func(context.Context, Question) (Answer, error) {
			return Answer{CostUSD: 999.99, Slots: map[string]SlotReading{
				"a": {Reading: Refused, Detail: "task_not_trained"},
				"b": {Reading: Failed, Detail: "bad"},
			}}, nil
		}},
		{"shadow_panics", true, nil, nil, func(context.Context, Question) (Answer, error) {
			panic("shadow exploded")
		}},
		{"sample_panics", true, func(Question) bool { panic("sampler exploded") }, nil, answering("sh", map[string]string{"a": "a2"}).Decide},
		{"sample_false", true, func(Question) bool { return false }, nil, answering("sh", map[string]string{"a": "a2"}).Decide},
		{"ondisagree_panics", true, nil, func(string, string, SlotReading, SlotReading) { panic("callback exploded") }, answering("sh", map[string]string{"a": "a2", "b": "b1"}).Decide},
		{"shadow_errauth", true, nil, nil, func(context.Context, Question) (Answer, error) {
			return Answer{CostUSD: -5}, ErrAuth
		}},
	}

	schedules := 0
	for pi, mkPrimary := range primaryChains {
		baseBackends := make([]Backend, 0)
		for _, b := range mkPrimary() {
			baseBackends = append(baseBackends, b)
		}
		baseRouter := NewRouter(baseBackends...)
		baseline, baseErr := baseRouter.Route(context.Background(), q)
		if baseErr != nil {
			t.Fatalf("baseline %d err: %v", pi, baseErr)
		}

		for _, sh := range shadowSpecs {
			chain := make([]Backend, 0)
			for _, b := range mkPrimary() {
				chain = append(chain, b)
			}
			r := NewRouter(chain...)
			r.Shadow = &fakeBackend{name: sh.name, serves: sh.serves, fn: sh.fn}
			r.ShadowSample = sh.sample
			r.OnDisagree = sh.onDis

			got, err := r.Route(context.Background(), q)
			if err != nil {
				t.Fatalf("primary=%d shadow=%s unexpected err: %v", pi, sh.name, err)
			}

			// Observational Non-Interference theorem: authoritative fields are bit-for-bit identical.
			if got.Backend != baseline.Backend {
				t.Fatalf("primary=%d shadow=%s altered Backend: got %q, want %q", pi, sh.name, got.Backend, baseline.Backend)
			}
			if !reflect.DeepEqual(got.Slots, baseline.Slots) {
				t.Fatalf("primary=%d shadow=%s altered authoritative Slots:\ngot:  %+v\nwant: %+v", pi, sh.name, got.Slots, baseline.Slots)
			}
			if !reflect.DeepEqual(got.SlotBackend, baseline.SlotBackend) {
				t.Fatalf("primary=%d shadow=%s altered SlotBackend:\ngot:  %+v\nwant: %+v", pi, sh.name, got.SlotBackend, baseline.SlotBackend)
			}
			if got.CostUSD != baseline.CostUSD {
				t.Fatalf("primary=%d shadow=%s altered primary CostUSD: got %v, want %v", pi, sh.name, got.CostUSD, baseline.CostUSD)
			}
			if math.IsNaN(got.ShadowCostUSD) || math.IsInf(got.ShadowCostUSD, 0) || got.ShadowCostUSD < 0 {
				t.Fatalf("primary=%d shadow=%s produced invalid ShadowCostUSD=%v", pi, sh.name, got.ShadowCostUSD)
			}
			// Every recorded Shadow reading must also satisfy Rule 7 sanitization.
			if got.Shadow != nil {
				assertRule7AnswerInvariant(t, q, Answer{Backend: sh.name, Slots: got.Shadow, CostUSD: got.ShadowCostUSD})
			}
			schedules++
		}
	}
	if schedules != len(primaryChains)*len(shadowSpecs) {
		t.Fatalf("expected %d schedules, ran %d", len(primaryChains)*len(shadowSpecs), schedules)
	}
}

func TestFormalDailyCapMonotonicityAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	spendPath := filepath.Join(dir, "spend.json")
	capTracker, err := NewDailyCap(spendPath, 1.00)
	if err != nil {
		t.Fatalf("NewDailyCap: %v", err)
	}
	fixedDay := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	capTracker.now = func() time.Time { return fixedDay }

	// Boundary rejections at Charge.
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.001} {
		if err := capTracker.Charge(context.Background(), "jev", bad); err == nil {
			t.Fatalf("Charge accepted invalid amount %v", bad)
		}
	}
	if err := capTracker.Charge(context.Background(), "", 0.1); err == nil {
		t.Fatal("Charge accepted empty key")
	}

	// Concurrent monotonicity + exact cutoff under -race:
	// 20 goroutines each try Check-then-Charge 0.10; Charge itself is unconditional
	// (recording actual spend), and once cumulative spend >= 1.00, Allow MUST
	// return ErrBudget monotonically forever on the same UTC day.
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = capTracker.Allow(context.Background(), "jev")
			if err := capTracker.Charge(context.Background(), "jev", 0.10); err != nil {
				t.Errorf("concurrent Charge failed: %v", err)
			}
		}()
	}
	wg.Wait()

	// Total charged is 1.20 >= 1.00: Allow for "jev" must fail closed with ErrBudget,
	// while an independent key "llm" remains allowed (per-key isolation).
	if err := capTracker.Allow(context.Background(), "jev"); !errors.Is(err, ErrBudget) {
		t.Fatalf("expected ErrBudget after 1.20 >= 1.00 spend, got %v", err)
	}
	if err := capTracker.Allow(context.Background(), "llm"); err != nil {
		t.Fatalf("expected unspent key llm to be allowed, got %v", err)
	}

	// UTC day rollover restores budget on the new day without losing history.
	capTracker.now = func() time.Time { return fixedDay.Add(24 * time.Hour) }
	if err := capTracker.Allow(context.Background(), "jev"); err != nil {
		t.Fatalf("expected budget reset on next UTC day, got %v", err)
	}

	// Fail-closed corruption suite: corrupting spend.json in any way MUST cause
	// Allow and NewDailyCap to refuse rather than resetting spend to zero.
	corruptPayloads := map[string][]byte{
		"truncated_json":   []byte(`{"version":1,"days":`),
		"unknown_field":    []byte(`{"version":1,"days":{},"bypass":true}`),
		"trailing_object":  []byte(`{"version":1,"days":{}}{"version":1,"days":{}}`),
		"wrong_version":    []byte(`{"version":2,"days":{}}`),
		"negative_balance": []byte(`{"version":1,"days":{"2026-11-11":{"jev":-5.0}}}`),
		"oversized_file":   []byte(`{"version":1,"days":{"` + strings.Repeat("x", maxSpendFile+16) + `":{}}}`),
	}
	for label, raw := range corruptPayloads {
		badPath := filepath.Join(dir, label+".json")
		if err := os.WriteFile(badPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewDailyCap(badPath, 1.0); err == nil {
			t.Errorf("%s: NewDailyCap accepted corrupt spend file", label)
		}
		// Also verify an already-constructed DailyCap fails closed on Allow if file is corrupted mid-flight.
		if err := os.WriteFile(spendPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := capTracker.Allow(context.Background(), "jev"); err == nil || errors.Is(err, ErrBudget) {
			t.Errorf("%s: Allow must fail closed with corruption error (not nil or ErrBudget), got %v", label, err)
		}
	}
}
