package decide

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrAuth is returned (wrapped) by a backend whose credentials were rejected or
// whose account has no credit. The Router treats the backend as unavailable for
// the rest of the process; retrying cannot help.
var ErrAuth = errors.New("decide: backend credentials rejected")

// RouterAnswer is the Router's merged result. Answer.Slots carry each slot's
// reading with Detail "backend=<name>"; SlotBackend repeats that as a map for
// callers that do not want to parse Detail.
type RouterAnswer struct {
	Answer
	// SlotBackend maps slot name to the backend whose reading it is. A slot no
	// backend was asked about has no entry.
	SlotBackend map[string]string `json:"slot_backend,omitempty"`
	// ShadowCostUSD is what the shadow backend cost; it is not part of CostUSD.
	ShadowCostUSD float64 `json:"shadow_cost_usd,omitzero"`
}

// Router asks backends in order, passing each only the slots earlier backends
// did not answer. It implements Decider.
type Router struct {
	// Shadow, when set, is asked the whole question alongside the primary
	// chain. Its answers never reach the result; they only drive OnDisagree.
	Shadow Backend
	// ShadowSample, when set, picks the questions the shadow is asked: false
	// leaves RouterAnswer.Shadow nil, as if no shadow were configured, so an
	// unsampled question is "not asked", never a failure. It is called after
	// Shadow.Serves, on the caller's goroutine, and a true return is always
	// followed by exactly one Shadow.Decide call, so it may reserve a slot that
	// the shadow releases when its call ends. A panic is contained and counts
	// as false and as a shadow error.
	ShadowSample func(Question) bool
	// OnDisagree is called once per slot where the shadow's reading differs
	// from the result's. It runs on the caller's goroutine; a panic is contained.
	OnDisagree func(task, slot string, primary, shadow SlotReading)
	// Timeout, when positive, bounds the whole Decide call in addition to ctx.
	Timeout time.Duration

	backends     []Backend
	mu           sync.Mutex
	disabled     map[int]bool
	shadowErrors atomic.Int64
}

var _ Decider = (*Router)(nil)

// NewRouter returns a Router over backends, asked in the order given.
func NewRouter(backends ...Backend) *Router {
	return &Router{backends: append([]Backend(nil), backends...), disabled: map[int]bool{}}
}

// ShadowErrors counts shadow calls that failed or whose answer was unusable.
func (r *Router) ShadowErrors() int64 { return r.shadowErrors.Load() }

// Backends names the chain's backends in the order they are asked. The shadow
// is not part of the chain.
func (r *Router) Backends() []string {
	out := make([]string, len(r.backends))
	for i, b := range r.backends {
		out[i] = b.Name()
	}
	return out
}

// Decide implements Decider.
func (r *Router) Decide(ctx context.Context, q Question) (Answer, error) {
	ra, err := r.Route(ctx, q)
	return ra.Answer, err
}

func (r *Router) isDisabled(i int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disabled[i]
}

func (r *Router) disable(i int) {
	r.mu.Lock()
	r.disabled[i] = true
	r.mu.Unlock()
}

// call runs one backend, containing a panic as an error.
func call(ctx context.Context, b Backend, q Question) (ans Answer, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("decide: backend %q panicked", b.Name())
		}
	}()
	return b.Decide(ctx, q)
}

func errDetail(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, ErrAuth):
		return "auth"
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return "deadline"
	default:
		return "error"
	}
}

func cleanCost(c float64) float64 {
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
		return 0
	}
	return c
}

// sanitize enforces the Reading contract on a backend's claim: an Answered
// value must be one of the slot's options (Rule 7), calibration must carry at
// most one backend pointer with finite [0, 1] scores and in-option conformal
// members, and nothing else carries a value or calibration.
func sanitize(s Slot, rd SlotReading, present bool) SlotReading {
	if !present {
		return SlotReading{Reading: Failed, Detail: "missing_slot"}
	}
	switch rd.Reading {
	case Answered:
		if checkValue(s, rd.Value) != nil {
			return SlotReading{Reading: Failed, Detail: "invalid_value"}
		}
		if rd.Lappi != nil && rd.Jev != nil {
			return SlotReading{Reading: Failed, Detail: "invalid_calibration"}
		}
		out := SlotReading{
			Reading: Answered,
			Value:   rd.Value,
			Detail:  rd.Detail,
		}
		if rd.Lappi != nil {
			if math.IsNaN(rd.Lappi.Score) || math.IsInf(rd.Lappi.Score, 0) || rd.Lappi.Score < 0 || rd.Lappi.Score > 1 {
				return SlotReading{Reading: Failed, Detail: "invalid_calibration"}
			}
			for _, m := range rd.Lappi.ConformalSet {
				if checkValue(s, m) != nil {
					return SlotReading{Reading: Failed, Detail: "invalid_calibration"}
				}
			}
			var clonedSet []string
			if rd.Lappi.ConformalSet != nil {
				clonedSet = append([]string(nil), rd.Lappi.ConformalSet...)
			}
			out.Lappi = &LappiCalib{
				Score:        rd.Lappi.Score,
				ConformalSet: clonedSet,
			}
		}
		if rd.Jev != nil {
			if math.IsNaN(rd.Jev.Probability) || math.IsInf(rd.Jev.Probability, 0) || rd.Jev.Probability < 0 || rd.Jev.Probability > 1 {
				return SlotReading{Reading: Failed, Detail: "invalid_calibration"}
			}
			out.Jev = &JevCalib{Probability: rd.Jev.Probability}
		}
		return out
	case Abstained, Refused, Failed, Unavailable, NotAsked:
		return SlotReading{Reading: rd.Reading, Detail: rd.Detail}
	default:
		return SlotReading{Reading: Failed, Detail: "unknown_reading"}
	}
}

type shadowResult struct {
	ans Answer
	err error
}

// Route is Decide with the per-slot backend map.
func (r *Router) Route(ctx context.Context, q Question) (RouterAnswer, error) {
	if err := ValidateQuestion(q); err != nil {
		return RouterAnswer{}, err
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	var shadowCh chan shadowResult
	if r.Shadow != nil && r.Shadow.Serves(q.Task) && r.sampled(q) {
		shadowCh = make(chan shadowResult, 1)
		go func() {
			a, err := call(ctx, r.Shadow, q)
			shadowCh <- shadowResult{a, err}
		}()
	}

	final := make(map[string]SlotReading, len(q.Slots))
	owner := make(map[string]string, len(q.Slots))
	pending := append([]Slot(nil), q.Slots...)
	var cost float64
	var answeredBy []string

	for i, b := range r.backends {
		if len(pending) == 0 || ctx.Err() != nil {
			break
		}
		if r.isDisabled(i) || !b.Serves(q.Task) {
			for _, s := range pending {
				if _, has := final[s.Name]; !has {
					final[s.Name] = SlotReading{Reading: NotAsked}
					owner[s.Name] = b.Name()
				}
			}
			continue
		}
		sub := q
		sub.Slots = pending
		ans, err := call(ctx, b, sub)
		cost += cleanCost(ans.CostUSD)
		if err != nil {
			if errors.Is(err, ErrAuth) {
				r.disable(i)
			}
			d := errDetail(ctx, err)
			for _, s := range pending {
				final[s.Name] = SlotReading{Reading: Unavailable, Detail: d}
				owner[s.Name] = b.Name()
			}
			continue
		}
		var still []Slot
		got := false
		for _, s := range pending {
			rd, present := ans.Slots[s.Name]
			rd = sanitize(s, rd, present)
			// A backend that declined to ask (NotAsked) must not overwrite a
			// more informative earlier reading.
			if prev, has := final[s.Name]; has && rd.Reading == NotAsked && prev.Reading != NotAsked {
				still = append(still, s)
				continue
			}
			final[s.Name] = rd
			owner[s.Name] = b.Name()
			if rd.Reading == Answered {
				got = true
				continue
			}
			still = append(still, s)
		}
		if got {
			answeredBy = append(answeredBy, b.Name())
		}
		pending = still
	}

	for _, s := range pending {
		switch prev, has := final[s.Name]; {
		case !has:
			final[s.Name] = SlotReading{Reading: NotAsked, Detail: "no_backend"}
			if ctx.Err() != nil {
				final[s.Name] = SlotReading{Reading: Unavailable, Detail: "deadline"}
			}
		case prev.Reading == NotAsked && ctx.Err() != nil:
			final[s.Name] = SlotReading{Reading: Unavailable, Detail: "deadline"}
			delete(owner, s.Name)
		}
	}

	out := RouterAnswer{
		Answer:      Answer{Backend: "router", Slots: final, CostUSD: cost},
		SlotBackend: owner,
	}
	if len(answeredBy) > 0 {
		out.Backend = strings.Join(answeredBy, "+")
	}
	for name, rd := range final {
		if b, ok := owner[name]; ok {
			if rd.Detail != "" {
				rd.Detail += ";"
			}
			rd.Detail += "backend=" + b
			final[name] = rd
		}
	}

	if shadowCh != nil {
		r.compareShadow(ctx, q, &out, shadowCh)
	}
	return out, nil
}

// shadowUnavailable records a shadow that was asked and gave no usable answer:
// every slot reads Unavailable with the error class, tagged like any shadow
// reading. A caller that counts how often the shadow was asked then sees the
// failures instead of only the answers.
func (r *Router) shadowUnavailable(q Question, detail string) map[string]SlotReading {
	m := make(map[string]SlotReading, len(q.Slots))
	for _, s := range q.Slots {
		m[s.Name] = SlotReading{Reading: Unavailable, Detail: detail + ";backend=" + r.Shadow.Name()}
	}
	return m
}

func (r *Router) compareShadow(ctx context.Context, q Question, out *RouterAnswer, ch chan shadowResult) {
	var res shadowResult
	select {
	case res = <-ch:
	case <-ctx.Done():
		r.shadowErrors.Add(1)
		out.Shadow = r.shadowUnavailable(q, errDetail(ctx, ctx.Err()))
		return
	}
	out.ShadowCostUSD = cleanCost(res.ans.CostUSD)
	if res.err != nil {
		r.shadowErrors.Add(1)
		out.Shadow = r.shadowUnavailable(q, errDetail(ctx, res.err))
		return
	}
	out.Shadow = make(map[string]SlotReading, len(q.Slots))
	for _, s := range q.Slots {
		rd, present := res.ans.Slots[s.Name]
		if !present {
			r.shadowErrors.Add(1)
		}
		sh := sanitize(s, rd, present)
		primary := out.Slots[s.Name]
		// Only a shadow that read the slot can disagree with the result; a
		// refused, failed or unavailable one is an outage, not an opinion.
		read := sh.Reading == Answered || sh.Reading == Abstained
		disagree := read && (sh.Reading != primary.Reading || sh.Value != primary.Value)
		// Tag the shadow's reading like a chain reading, so a caller that
		// records it knows which backend it measures.
		if sh.Detail != "" {
			sh.Detail += ";"
		}
		sh.Detail += "backend=" + r.Shadow.Name()
		out.Shadow[s.Name] = sh
		if !disagree {
			continue
		}
		if r.OnDisagree != nil {
			r.fire(q.Task, s.Name, primary, sh)
		}
	}
}

// sampled reports whether ShadowSample (nil: every question) picks q.
func (r *Router) sampled(q Question) (ok bool) {
	if r.ShadowSample == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			r.shadowErrors.Add(1)
			ok = false
		}
	}()
	return r.ShadowSample(q)
}

func (r *Router) fire(task, slot string, primary, shadow SlotReading) {
	defer func() {
		if recover() != nil {
			r.shadowErrors.Add(1)
		}
	}()
	r.OnDisagree(task, slot, primary, shadow)
}
