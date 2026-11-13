package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/wouldapply"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

var pairT0 = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

func pairIdent() PairIdent {
	return PairIdent{Model: "model-x", Fixture: strings.Repeat("f", 64), Prompt: strings.Repeat("p", 64), Exemplar: strings.Repeat("e", 64)}
}

func pairPosting(key string) posting.Posting {
	return posting.Posting{Key: posting.Key(key), Title: "Title " + key, Location: "Remote", Description: "description of " + key}
}

func pairWork(a, b string) PairWork {
	pa, pb := pairPosting(a), pairPosting(b)
	q, _, err := wouldapply.Question(pa, pb, "summary", wouldapply.Exemplars{
		Yes: []wouldapply.Exemplar{{Title: "yes one"}}, No: []wouldapply.Exemplar{{Title: "no one"}}})
	if err != nil {
		panic(err)
	}
	return PairWork{A: pa, B: pb, Stratum: "decile:5", Q: q}
}

func pairRowOf(a, b, answer string, at time.Time) PairRow {
	w := pairWork(a, b)
	c := wouldapply.Choice{Reading: decide.Answered, Value: answer}
	if answer == "" {
		c = wouldapply.Choice{Reading: decide.Abstained}
	}
	return pairRow(pairIdent(), w, c, at)
}

func TestPairStoreKeepsAnswersWholeAndRepairsATornTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), PairsFile)
	s, err := OpenPairStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatal("a missing file is not an empty store")
	}
	r1, r2 := pairRowOf("k:1", "k:2", "a", pairT0), pairRowOf("k:3", "k:4", "neither", pairT0)
	if err := s.Append(r1); err != nil {
		t.Fatal(err)
	}
	// A crash mid-write leaves a torn tail; the next row must not be glued onto it.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"v":1,"task":"career.would_a`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err = OpenPairStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 || s.Skipped != 1 {
		t.Fatalf("rows %d skipped %d, want 1 and 1", s.Len(), s.Skipped)
	}
	if err := s.Append(r2); err != nil {
		t.Fatal(err)
	}
	s, err = OpenPairStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("%d rows after the repair, want 2 (the new row was glued to the torn one)", s.Len())
	}
	if !s.Has(pairIdent(), "k:1", "k:2", r1.RenderSHA256) || s.Has(pairIdent(), "k:1", "k:3", r1.RenderSHA256) {
		t.Error("Has does not tell stored pairs from others")
	}
	other := pairIdent()
	other.Model = "model-y"
	if s.Has(other, "k:1", "k:2", r1.RenderSHA256) {
		t.Error("another model's answer counts as this model's")
	}
}

func TestPairStoreSkipsHandEditsAndOversizedLines(t *testing.T) {
	good := pairRowOf("k:1", "k:2", "b", pairT0)
	line := func(r PairRow) string {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw) + "\n"
	}
	swapLie := good
	swapLie.Swapped = !swapLie.Swapped
	noAnswer := good
	noAnswer.Answer = ""
	badAnswer := good
	badAnswer.Answer = "c"
	self := good
	self.KeyB = self.KeyA
	abstainedWithAnswer := pairRowOf("k:5", "k:6", "", pairT0)
	abstainedWithAnswer.Answer = "a"
	noModel := good
	noModel.Model = ""
	otherTask := good
	otherTask.Task = "career.kb_match"
	future := good
	future.V = 2
	content := line(good) + line(swapLie) + line(noAnswer) + line(badAnswer) + line(self) + line(abstainedWithAnswer) + line(noModel) + line(otherTask) + line(future) +
		"not json\n\n" + strings.Repeat("x", maxPairLine+10) + "\n" + line(pairRowOf("k:7", "k:8", "a", pairT0))
	path := filepath.Join(t.TempDir(), PairsFile)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenPairStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Errorf("%d rows kept, want the 2 valid ones", s.Len())
	}
	if s.Skipped != 10 {
		t.Errorf("%d lines skipped, want 10", s.Skipped)
	}
}

func TestPairAppendRefusesAnInvalidRow(t *testing.T) {
	s, _ := OpenPairStore(filepath.Join(t.TempDir(), PairsFile))
	bad := pairRowOf("k:1", "k:2", "a", pairT0)
	bad.KeyB = bad.KeyA
	if err := s.Append(bad); err == nil {
		t.Error("a pair of one posting was stored")
	}
	if s.Len() != 0 {
		t.Error("a refused row is in the store")
	}
}

// pairFake is a decider that answers from a script and counts what it is asked.
type pairFake struct {
	mu     sync.Mutex
	asked  int
	answer func(n int, q decide.Question) (decide.Answer, error)
	meter  atomic.Int64
	cost   int64
}

func (f *pairFake) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	f.mu.Lock()
	f.asked++
	n := f.asked
	f.mu.Unlock()
	f.meter.Add(f.cost)
	return f.answer(n, q)
}

func answerWith(v string) func(int, decide.Question) (decide.Answer, error) {
	return func(int, decide.Question) (decide.Answer, error) {
		rd := decide.SlotReading{Reading: decide.Answered, Value: v}
		if v == "none" {
			rd = decide.SlotReading{Reading: decide.Abstained}
		}
		return decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{wouldapply.Slot: rd}}, nil
	}
}

func newPairRun(t *testing.T, d decide.Decider, limit int64) (PairRun, *PairStore, *atomic.Int64) {
	t.Helper()
	s, err := OpenPairStore(filepath.Join(t.TempDir(), PairsFile))
	if err != nil {
		t.Fatal(err)
	}
	var metered atomic.Int64
	f, _ := d.(*pairFake)
	return PairRun{
		D: d, Ident: pairIdent(), Store: s, Concurrency: 1, LimitNano: limit,
		Spent: func(context.Context) (int64, error) {
			if f != nil {
				return f.meter.Load(), nil
			}
			return 0, nil
		},
		Worst: func(decide.Question) int64 { return 100 },
		Meter: func(context.Context) error { metered.Add(1); return nil },
		Now:   func() time.Time { return pairT0 },
	}, s, &metered
}

func TestPairRunStoresEveryAnswerAndMetersEachQuestion(t *testing.T) {
	f := &pairFake{answer: answerWith("b"), cost: 10}
	run, s, metered := newPairRun(t, f, 1_000_000)
	res, err := run.Run(context.Background(), []PairWork{pairWork("k:1", "k:2"), pairWork("k:3", "k:4"), pairWork("k:5", "k:6")})
	if err != nil || res.Stopped != "" {
		t.Fatalf("err %v stopped %q", err, res.Stopped)
	}
	if res.Asked != 3 || res.Stored != 3 || s.Len() != 3 {
		t.Errorf("asked %d stored %d rows %d, want 3 each", res.Asked, res.Stored, s.Len())
	}
	if metered.Load() != 3 {
		t.Errorf("the spend was recorded %d times, want once per question", metered.Load())
	}
	for _, r := range s.Rows() {
		if r.Answer != "b" || r.Model != "model-x" || r.ExemplarSHA == "" || r.RenderSHA256 == "" || r.Stratum != "decile:5" {
			t.Errorf("row %+v", r)
		}
	}
}

func TestPairRunAsksADuplicateOnceAndResumesWithoutRepeating(t *testing.T) {
	f := &pairFake{answer: answerWith("a")}
	run, s, _ := newPairRun(t, f, 1_000_000)
	w := pairWork("k:1", "k:2")
	if _, err := run.Run(context.Background(), []PairWork{w, w, w}); err != nil {
		t.Fatal(err)
	}
	if f.asked != 1 || s.Len() != 1 {
		t.Fatalf("a repeated pair was asked %d times, stored %d", f.asked, s.Len())
	}
	res, err := run.Run(context.Background(), []PairWork{w, pairWork("k:3", "k:4")})
	if err != nil {
		t.Fatal(err)
	}
	if f.asked != 2 || res.Skipped != 1 || res.Stored != 1 {
		t.Errorf("a resumed run asked %d in all, skipped %d, stored %d", f.asked, res.Skipped, res.Stored)
	}
	// The same pair in the other order is a different question.
	if _, err := run.Run(context.Background(), []PairWork{pairWork("k:2", "k:1")}); err != nil || f.asked != 3 {
		t.Errorf("the reverse order was not asked (asked %d, err %v)", f.asked, err)
	}
}

func TestPairRunRefusesAPairOfOnePosting(t *testing.T) {
	f := &pairFake{answer: answerWith("a")}
	run, s, _ := newPairRun(t, f, 1_000_000)
	w := pairWork("k:1", "k:2")
	w.B = w.A
	if _, err := run.Run(context.Background(), []PairWork{w}); err != nil {
		t.Fatal(err)
	}
	if f.asked != 0 || s.Len() != 0 {
		t.Errorf("a pair of one posting was asked %d times and stored %d", f.asked, s.Len())
	}
}

func TestPairRunKeepsAnAbstentionAndAReplyThatIsNeitherOption(t *testing.T) {
	f := &pairFake{answer: answerWith("none")}
	run, s, _ := newPairRun(t, f, 1_000_000)
	res, err := run.Run(context.Background(), []PairWork{pairWork("k:1", "k:2"), pairWork("k:3", "k:4"), pairWork("k:5", "k:6"), pairWork("k:7", "k:8")})
	if err != nil || res.Stopped != "" || res.Abstained != 4 || s.Len() != 4 {
		t.Fatalf("abstentions: err %v res %+v rows %d (an abstention is an answer, not a failure)", err, res, s.Len())
	}
	for _, r := range s.Rows() {
		if r.Answer != "" || r.Reading != string(decide.Abstained) {
			t.Errorf("an abstention was stored as %+v", r)
		}
	}

	// A model that answers neither option every time: stored as unusable, and the
	// run stops instead of paying for the rest.
	g := &pairFake{answer: answerWith("maybe a")}
	run2, s2, _ := newPairRun(t, g, 1_000_000)
	var work []PairWork
	for i := range 10 {
		work = append(work, pairWork(fmt.Sprintf("k:%d", 2*i), fmt.Sprintf("k:%d", 2*i+1)))
	}
	res2, err := run2.Run(context.Background(), work)
	if err == nil && res2.Stopped == "" {
		t.Fatal("a model that never answers a listed option did not stop the run")
	}
	if g.asked != TeacherStopAfter || res2.Unusable != TeacherStopAfter || s2.Len() != TeacherStopAfter {
		t.Errorf("asked %d, unusable %d, stored %d, want %d each", g.asked, res2.Unusable, s2.Len(), TeacherStopAfter)
	}
	for _, r := range s2.Rows() {
		if r.Answer != "" || r.Reading != string(decide.Failed) || r.Detail != "invalid_value" {
			t.Errorf("an unlisted reply was stored as %+v", r)
		}
	}
}

func TestPairRunStopsWhenTheProviderIsDown(t *testing.T) {
	down := errors.New("provider down")
	f := &pairFake{answer: func(int, decide.Question) (decide.Answer, error) { return decide.Answer{}, down }}
	run, s, metered := newPairRun(t, f, 1_000_000)
	var work []PairWork
	for i := range 10 {
		work = append(work, pairWork(fmt.Sprintf("k:%d", 2*i), fmt.Sprintf("k:%d", 2*i+1)))
	}
	res, err := run.Run(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if f.asked != TeacherStopAfter || res.Unread != TeacherStopAfter || s.Len() != 0 || !strings.Contains(res.Stopped, "no usable reply") {
		t.Errorf("asked %d unread %d stored %d stopped %q", f.asked, res.Unread, s.Len(), res.Stopped)
	}
	if metered.Load() != int64(TeacherStopAfter) {
		t.Errorf("a question that failed after being sent was metered %d times, want %d", metered.Load(), TeacherStopAfter)
	}
}

func TestPairRunNeverPassesItsSpendLimit(t *testing.T) {
	f := &pairFake{answer: answerWith("a"), cost: 40}
	// Each question's worst case is 100 and it really costs 40: a question is sent
	// while what was spent plus its worst case fits 250, so four are (160 spent,
	// and the fifth could reach 260).
	run, s, _ := newPairRun(t, f, 250)
	var work []PairWork
	for i := range 6 {
		work = append(work, pairWork(fmt.Sprintf("k:%d", 2*i), fmt.Sprintf("k:%d", 2*i+1)))
	}
	res, err := run.Run(context.Background(), work)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != TeacherStoppedAtLimit || f.asked != 4 || s.Len() != 4 || f.meter.Load() > 250 {
		t.Errorf("stopped %q asked %d stored %d spent %d", res.Stopped, f.asked, s.Len(), f.meter.Load())
	}

	// A limit under one question's worst case asks nothing at all.
	g := &pairFake{answer: answerWith("a")}
	run2, _, _ := newPairRun(t, g, 99)
	res2, err := run2.Run(context.Background(), work)
	if err != nil || g.asked != 0 || res2.Stopped != TeacherStoppedAtLimit {
		t.Errorf("a limit under the worst case still asked %d (err %v, stopped %q)", g.asked, err, res2.Stopped)
	}
}

func TestPairRunStopsWhenTheSpendCannotBeRecorded(t *testing.T) {
	f := &pairFake{answer: answerWith("a")}
	run, s, _ := newPairRun(t, f, 1_000_000)
	run.Meter = func(context.Context) error { return errors.New("disk full") }
	work := []PairWork{pairWork("k:1", "k:2"), pairWork("k:3", "k:4"), pairWork("k:5", "k:6")}
	res, err := run.Run(context.Background(), work)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err %v", err)
	}
	if f.asked != 1 || res.Stopped == "" {
		t.Errorf("asked %d after the spend record failed, stopped %q", f.asked, res.Stopped)
	}
	if s.Len() != 1 {
		t.Errorf("the paid-for answer was not kept (%d rows)", s.Len())
	}
}

func TestPairRunNeedsItsGuards(t *testing.T) {
	f := &pairFake{answer: answerWith("a")}
	base, _, _ := newPairRun(t, f, 1000)
	for name, mut := range map[string]func(*PairRun){
		"no decider":     func(r *PairRun) { r.D = nil },
		"no store":       func(r *PairRun) { r.Store = nil },
		"no meter":       func(r *PairRun) { r.Meter = nil },
		"no spend":       func(r *PairRun) { r.Spent = nil },
		"no worst":       func(r *PairRun) { r.Worst = nil },
		"no identity":    func(r *PairRun) { r.Ident.Exemplar = "" },
		"no concurrency": func(r *PairRun) { r.Concurrency = 0 },
		"too many":       func(r *PairRun) { r.Concurrency = MaxTeacherConcurrency + 1 },
		"no limit":       func(r *PairRun) { r.LimitNano = 0 },
	} {
		r := base
		mut(&r)
		if _, err := r.Run(context.Background(), []PairWork{pairWork("k:1", "k:2")}); err == nil {
			t.Errorf("%s: the run started", name)
		}
	}
	if f.asked != 0 {
		t.Errorf("a refused run asked %d questions", f.asked)
	}
	if _, err := base.Run(context.Background(), make([]PairWork, 2*MaxPairsPerRun+1)); err == nil {
		t.Error("an oversized run started")
	}
}

func TestPairRunStopsOnACancelledContext(t *testing.T) {
	f := &pairFake{answer: answerWith("a")}
	run, _, _ := newPairRun(t, f, 1_000_000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := run.Run(ctx, []PairWork{pairWork("k:1", "k:2")})
	if f.asked != 0 {
		t.Errorf("a cancelled run asked %d questions", f.asked)
	}
	if err == nil && res.Stopped == "" {
		t.Error("a cancelled run looks like a completed one")
	}
}

func TestLockPairsIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), PairsFile)
	unlock, err := LockPairs(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockPairs(context.Background(), path, 100*time.Millisecond); !errors.Is(err, ErrPairsBusy) {
		t.Errorf("a second run took the lock: %v", err)
	}
	unlock()
	again, err := LockPairs(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}
