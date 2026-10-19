package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The sample is a pure function of the posting key: the same posting is in
// or out on every run, every=1 takes everything, and the rate is one in every.
func TestShadowSampleIsAStableOneInEvery(t *testing.T) {
	const n, every = 12000, 12
	in := 0
	for i := range n {
		k := fmt.Sprintf("gh:acme:%d", i)
		got := inSample(k, every)
		if got != inSample(k, every) {
			t.Fatalf("%s sampled differently on a second ask", k)
		}
		if got {
			in++
		}
		if !inSample(k, 1) || !inSample(k, 0) {
			t.Fatalf("%s not sampled with every <= 1", k)
		}
	}
	if lo, hi := n/every*8/10, n/every*12/10; in < lo || in > hi {
		t.Fatalf("%d of %d sampled at one in %d, want %d..%d", in, n, every, lo, hi)
	}
}

// A sampled question is skipped, and counted, while the shadow's in-flight
// limit is full; the slot comes back when the shadow call ends.
func TestShadowGateSkipsWhileBusy(t *testing.T) {
	g := newShadowGate(1, 1)
	q := decide.Question{RecordID: "gh:a:1"}
	if !g.sample(q) {
		t.Fatal("first sampled question refused")
	}
	if g.sample(q) {
		t.Fatal("second question asked while the one slot was held")
	}
	g.release()
	if !g.sample(q) {
		t.Fatal("slot not returned after the call")
	}
	g.release()
	g.release() // a call that held no slot does not block or free one it lacks
	if c, a, b := g.considered.Load(), g.asked.Load(), g.busy.Load(); c != 3 || a != 2 || b != 1 {
		t.Fatalf("considered %d asked %d busy %d", c, a, b)
	}
	unlimited := newShadowGate(1, 0)
	for range 100 {
		if !unlimited.sample(q) {
			t.Fatal("a shadow with no in-flight limit was refused")
		}
	}
}

func TestShadowInFlight(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    config.Decide
		want int
	}{
		{"no shadow", config.Decide{}, 0},
		{"openrouter shadow", config.Decide{Shadow: "openrouter"}, 0},
		{"lappi shadow", config.Decide{Shadow: "lappi"}, 0},
		{"local endpoint", config.Decide{Shadow: "local", Local: &config.Local{Endpoint: "http://127.0.0.1:1"}}, 2},
		{"local server auto", config.Decide{Shadow: "local", Local: &config.Local{Server: &config.LocalServer{}}}, 2},
		{"local server parallel 3", config.Decide{Shadow: "local", Local: &config.Local{Server: &config.LocalServer{Parallel: 3}}}, 4},
		{"local configured, another shadow", config.Decide{Shadow: "openrouter", Local: &config.Local{Endpoint: "http://127.0.0.1:1"}}, 0},
	} {
		if got := shadowInFlight(tc.d); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

// triageView is what a decision consists of: status and every non-shadow
// triage annotation.
func triageView(l *eventLedger) map[posting.Key]string {
	out := map[posting.Key]string{}
	for _, r := range l.All() {
		var b strings.Builder
		b.WriteString(string(r.Status))
		for _, k := range slices.Sorted(maps.Keys(r.Annotations)) {
			if strings.Contains(k, ".shadow") || strings.HasSuffix(k, ".disagree") {
				continue
			}
			fmt.Fprintf(&b, "|%s=%s", k, r.Annotations[k])
		}
		out[r.Key] = b.String()
	}
	return out
}

// A run with the gated shadow makes exactly the decisions a run without a
// shadow makes. Sampled, asked postings carry the shadow's readings tagged
// with its name; every other posting carries none, which `vani eval` reads as
// not asked.
func TestGatedShadowChangesNoDecision(t *testing.T) {
	const n, every = 96, 4
	run := func(shadow bool) (*eventLedger, *pipeline.Runner) {
		l := benchLedger(t, n)
		rt := decide.NewRouter(&sleepBackend{name: "d1-ultra", delay: 200 * time.Microsecond})
		if shadow {
			// The shadow disagrees on every slot, so a leak into the decision shows.
			rt.Shadow = &disagreeing{sleepBackend{name: "local-vllm-d3-mini-combined", delay: 2 * time.Millisecond, serial: true}}
			gateShadow(rt, every, 1)
		}
		r := &pipeline.Runner{Ledger: l, Triager: &jdtriage.Triager{D: rt}, SkipScoring: true, SkipExplain: true}
		if _, err := advanceAll(context.Background(), l.All(), nil, n, 4, nil, r.Advance, func(string, ...any) {}); err != nil {
			t.Fatal(err)
		}
		return l, r
	}
	plain, _ := run(false)
	gated, r := run(true)
	if a, b := triageView(plain), triageView(gated); !maps.Equal(a, b) {
		for k := range a {
			if a[k] != b[k] {
				t.Fatalf("%s decided differently with the shadow:\n without %s\n with    %s", k, a[k], b[k])
			}
		}
		t.Fatal("decisions differ")
	}
	g := r.Triager.(*jdtriage.Triager).D.(*decide.Router).Shadow.(gatedShadow).gate
	shadowed := 0
	for _, rec := range gated.All() {
		readings := 0
		for _, s := range jdtriage.Slots {
			pre := ledger.AnnTriagePrefix + s.Name
			if rec.Annotations[pre+ledger.AnnTriageSuffixShadowReading] != "" {
				readings++
				if got := rec.Annotations[pre+ledger.AnnTriageSuffixShadowBackend]; got != "local-vllm-d3-mini-combined" {
					t.Fatalf("%s shadow backend %q", rec.Key, got)
				}
			}
		}
		switch {
		case readings == len(jdtriage.Slots):
			shadowed++
			if !inSample(string(rec.Key), every) {
				t.Fatalf("%s shadowed but not in the sample", rec.Key)
			}
		case readings != 0:
			t.Fatalf("%s has %d of %d shadow readings", rec.Key, readings, len(jdtriage.Slots))
		}
	}
	if int64(shadowed) != g.asked.Load() || g.asked.Load()+g.busy.Load() != int64(countSampled(n, every)) || g.considered.Load() != n {
		t.Fatalf("shadowed %d, gate considered %d asked %d busy %d, sample %d", shadowed, g.considered.Load(), g.asked.Load(), g.busy.Load(), countSampled(n, every))
	}
	if shadowed == 0 {
		t.Fatal("no posting was shadowed")
	}
	note := shadowSampleNote(r)
	if !strings.Contains(note, fmt.Sprintf("asked about %d of %d posting(s) (one in %d sampled)", shadowed, n, every)) {
		t.Fatalf("note %q", note)
	}
}

func countSampled(n, every int) int {
	c := 0
	for i := range n {
		if inSample(fmt.Sprintf("gh:acme:%03d", i), every) {
			c++
		}
	}
	return c
}

// disagreeing answers every slot with its last option.
type disagreeing struct{ sleepBackend }

func (d *disagreeing) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	a, err := d.sleepBackend.Decide(ctx, q)
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[len(s.Options)-1]}
	}
	return a, err
}

func TestShadowSampleNoteWithoutAGate(t *testing.T) {
	if shadowSampleNote(nil) != "" || shadowSampleNote(&pipeline.Runner{}) != "" {
		t.Fatal("note without a router")
	}
	rt := decide.NewRouter(shadowTestPrimary{})
	if shadowSampleNote(&pipeline.Runner{Triager: &jdtriage.Triager{D: rt}}) != "" {
		t.Fatal("note without a shadow")
	}
	if gateShadow(rt, 12, 1) != nil || rt.ShadowSample != nil {
		t.Fatal("gated a router with no shadow")
	}
	rt.Shadow = shadowTestPrimary{}
	gateShadow(rt, 12, 1)
	if shadowSampleNote(&pipeline.Runner{Triager: &jdtriage.Triager{D: rt}}) != "" {
		t.Fatal("note before any posting was offered")
	}
}
