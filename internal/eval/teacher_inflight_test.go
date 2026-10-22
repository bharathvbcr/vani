package eval

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/decide"
)

// The gate admits on Worst(), so Run's overshoot is exactly the amount by which
// a question exceeds Worst(). Run itself cannot know that excess; the bound is
// the caller's to make a true upper bound (cmd/vani teacherWorstNano uses the
// physical ceiling, tokens <= bytes). With a truthful bound the spend never
// passes the limit however many questions are in flight at once.
func TestTruthfulWorstCaseNeverOvershootsAtFullConcurrency(t *testing.T) {
	const W = 1000
	limit := int64(16 * W)
	for _, actual := range []int64{W, W / 2, 1} {
		started, spent, peak, _ := advRun(t, MaxTeacherConcurrency, limit, W, actual, 64)
		t.Logf("limit %d, worst-case bound %d, actual per question %d: started %d, peak in flight %d, spent %d", limit, W, actual, started, peak, spent)
		if spent > limit {
			t.Fatalf("spend %d passed the limit %d with a bound (%d) at or above the actual cost (%d)", spent, limit, W, actual)
		}
		if started == 0 {
			t.Fatal("nothing was asked")
		}
	}
}

// Reaching the limit stops the issuing of questions but never cancels those
// already in flight: a real provider bills what it generated, so each paid
// answer is stored rather than discarded and paid for again on resume. The fake
// bills every question it is handed.
func TestQuestionsPaidForAtTheLimitAreStoredNotDiscarded(t *testing.T) {
	const W = 1000
	var meter, paid atomic.Int64
	d := deciderFn(func(ctx context.Context, q decide.Question) (decide.Answer, error) {
		paid.Add(1)
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
		}
		meter.Add(W)
		if ctx.Err() != nil {
			return decide.Answer{}, ctx.Err()
		}
		return advAnswer(q), nil
	})
	st, err := OpenTeacherStore(t.TempDir() + "/r.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	run := TeacherRun{
		D: d, Model: "m", Summary: "s", Store: st, Concurrency: 8, LimitNano: 3 * W,
		Spent: func(context.Context) (int64, error) { return meter.Load(), nil },
		Worst: func(decide.Question) int64 { return W },
	}
	res, err := run.Run(context.Background(), advCands(20))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("paid for %d questions (meter %d), stored %d, stopped %q", paid.Load(), meter.Load(), res.Stored, res.Stopped)
	if res.Stopped != TeacherStoppedAtLimit {
		t.Errorf("stopped %q, want the limit", res.Stopped)
	}
	if int64(res.Stored) != paid.Load() || st.Len() != res.Stored {
		t.Errorf("%d questions were billed, %d readings reported stored, %d in the log", paid.Load(), res.Stored, st.Len())
	}
	if meter.Load() > 3*W {
		t.Errorf("spent %d past the limit %d", meter.Load(), 3*W)
	}
}

// The caller's interrupt (Ctrl-C) still cancels questions in flight, promptly,
// and nothing is stored from a cancelled question.
func TestInterruptStillCancelsQuestionsInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var started atomic.Int64
	d := deciderFn(func(ctx context.Context, q decide.Question) (decide.Answer, error) {
		if started.Add(1) == 4 {
			cancel()
		}
		<-ctx.Done()
		return decide.Answer{}, ctx.Err()
	})
	st, err := OpenTeacherStore(t.TempDir() + "/r.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	run := TeacherRun{
		D: d, Model: "m", Summary: "s", Store: st, Concurrency: 4, LimitNano: 1 << 40,
		Spent: func(context.Context) (int64, error) { return 0, nil },
		Worst: func(decide.Question) int64 { return 1 },
	}
	done := make(chan TeacherResult, 1)
	go func() {
		res, _ := run.Run(ctx, advCands(40))
		done <- res
	}()
	select {
	case res := <-done:
		if res.Stopped != "interrupted" || res.Stored != 0 {
			t.Errorf("stopped %q, stored %d", res.Stopped, res.Stored)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an interrupt did not end the run")
	}
}

func advAnswer(q decide.Question) decide.Answer {
	a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return a
}

type deciderFn func(context.Context, decide.Question) (decide.Answer, error)

func (f deciderFn) Decide(ctx context.Context, q decide.Question) (decide.Answer, error) {
	return f(ctx, q)
}
