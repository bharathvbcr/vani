package eval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

const (
	tkSecret = "VAULT-NOTE-SENTINEL-7731"
	tkCost   = int64(100)
)

func tkRec(key, backend, desc string, ann map[string]string) ledger.Record {
	a := map[string]string{"triage.role_fit.backend": backend, "triage.role_fit": "adjacent", "triage.role_fit.reading": "answered"}
	for k, v := range ann {
		a[k] = v
	}
	return ledger.Record{
		Key: posting.Key(key), Status: posting.Triaged, Annotations: a,
		Posting: posting.Posting{Key: posting.Key(key), Company: "SynthCo", Title: "Engineer " + key, Location: "Nowhere", Description: desc},
	}
}

// tkDecider answers every slot "answered" with the first option, counts calls,
// charges tkCost to the shared meter and remembers each context it was sent.
type tkDecider struct {
	mu       sync.Mutex
	calls    int
	contexts []string
	meter    *atomic.Int64
	reading  decide.Reading
	err      error
	onCall   func(n int)
}

func (d *tkDecider) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.contexts = append(d.contexts, string(q.Context)+"\n"+q.Prompt)
	d.mu.Unlock()
	d.meter.Add(tkCost)
	if d.onCall != nil {
		d.onCall(n)
	}
	if d.err != nil {
		return decide.Answer{}, d.err
	}
	rd := d.reading
	if rd == "" {
		rd = decide.Answered
	}
	a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		sr := decide.SlotReading{Reading: rd}
		if rd == decide.Answered {
			sr.Value = s.Options[0]
		} else if rd == decide.Failed {
			sr.Detail = "unparseable"
		}
		a.Slots[s.Name] = sr
	}
	return a, nil
}

func tkRun(t *testing.T, d *tkDecider, meter *atomic.Int64, limit int64, conc int) (TeacherRun, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), TeacherFile)
	st, err := OpenTeacherStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return TeacherRun{
		D: d, Model: "claude-haiku-test", Summary: "ml systems roles", Store: st, Concurrency: conc, LimitNano: limit,
		Spent: func(context.Context) (int64, error) { return meter.Load(), nil },
		Worst: func(decide.Question) int64 { return tkCost },
		Now:   func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) },
	}, path
}

func tkCands(n int) []TeacherCandidate {
	var out []TeacherCandidate
	for i := range n {
		k := posting.Key("gh:synth:" + string(rune('a'+i)))
		out = append(out, TeacherCandidate{Posting: posting.Posting{Key: k, Title: "T" + string(k), Description: "duties for " + string(k)}})
	}
	return out
}

func TestTeacherCandidatesSelectsWhatTheFirstTeacherRead(t *testing.T) {
	inherited := tkRec("gh:synth:inh", "d1-ultra", "text", map[string]string{ledger.AnnTriageInheritedFrom: "gh:synth:a"})
	dropped := tkRec("gh:synth:drop", "d1-ultra", "dropped text", nil)
	dropped.Status = posting.Dropped
	evicted := tkRec("gh:synth:evict", "jev", "", nil) // legacy name, body evicted
	evicted.BodyEvicted = true
	recs := []ledger.Record{
		tkRec("gh:synth:a", "d1-ultra", "text a", nil),
		tkRec("gh:synth:a", "d1-ultra", "text a", nil), // duplicate key
		tkRec("gh:synth:llm", "llm", "text", nil),
		tkRec("gh:synth:shadow", "llm", "text", map[string]string{"triage.role_fit.shadow_backend": "d1-ultra"}),
		tkRec("gh:synth:empty", "d1-ultra", "  ", nil),
		inherited, dropped, evicted,
	}
	full := func(r ledger.Record) ledger.Record {
		if r.BodyEvicted {
			r.Posting.Description, r.BodyEvicted = "read back from the ledger", false
		}
		return r
	}
	sel := TeacherCandidates(recs, Names{OpenRouter: "d1-ultra"}, "d1-ultra", full)
	var got []string
	for _, c := range sel.Candidates {
		got = append(got, string(c.Posting.Key)+"="+c.Posting.Description)
	}
	want := "gh:synth:a=text a,gh:synth:drop=dropped text,gh:synth:evict=read back from the ledger"
	if strings.Join(got, ",") != want {
		t.Errorf("candidates = %v, want %s", got, want)
	}
	if sel.NoText != 1 || sel.Inherited != 1 {
		t.Errorf("left out: no text %d inherited %d, want 1 and 1", sel.NoText, sel.Inherited)
	}
}

func TestTeacherRunStoresProvenanceAndNothingPrivate(t *testing.T) {
	var meter atomic.Int64
	d := &tkDecider{meter: &meter}
	run, path := tkRun(t, d, &meter, 10*tkCost, 2)
	cands := tkCands(3)
	// Personal notes live in the record's annotations and marks, never in the posting
	// text the question is built from.
	res, err := run.Run(context.Background(), cands)
	if err != nil || res.Stopped != "" || res.Stored != 3 || res.Asked != 3 {
		t.Fatalf("run = %+v, %v", res, err)
	}
	for _, c := range d.contexts {
		if strings.Contains(c, tkSecret) {
			t.Fatalf("a prompt carries private data: %q", c)
		}
		if !strings.Contains(c, "ml systems roles") || !strings.Contains(c, "duties for") {
			t.Errorf("prompt lacks the target summary or posting text: %q", c)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tkSecret) || strings.Contains(string(raw), "duties for") {
		t.Errorf("the log holds private data or posting text:\n%s", raw)
	}
	st, err := OpenTeacherStore(path)
	if err != nil || st.Len() != 3 || st.Skipped != 0 {
		t.Fatalf("reopen: len %d skipped %d err %v", st.Len(), st.Skipped, err)
	}
	id := TeacherIdent{Text: HashText(cands[0].Posting.Description), Model: "claude-haiku-test", Fixture: FixtureSHA(), Prompt: PromptHash("ml systems roles")}
	if !st.Has(cands[0].Posting.Key, id) {
		t.Error("the stored reading is not found under its provenance")
	}
	for _, mut := range []func(*TeacherIdent){
		func(i *TeacherIdent) { i.Text = HashText("other text") },
		func(i *TeacherIdent) { i.Model = "other" },
		func(i *TeacherIdent) { i.Fixture = "other" },
		func(i *TeacherIdent) { i.Prompt = PromptHash("another summary") },
	} {
		j := id
		mut(&j)
		if st.Has(cands[0].Posting.Key, j) {
			t.Errorf("a reading under a different identity %+v counts as current", j)
		}
	}
	if len(jdtriage.FixtureJSON) == 0 || len(FixtureSHA()) != 64 {
		t.Error("fixture sha not computed")
	}
}

func TestTeacherRunKeepsUnusableAnswers(t *testing.T) {
	var meter atomic.Int64
	d := &tkDecider{meter: &meter, reading: decide.Failed}
	run, path := tkRun(t, d, &meter, 10*tkCost, 1)
	res, err := run.Run(context.Background(), tkCands(2))
	if err != nil || res.Stored != 2 || res.Unusable != 2 || res.Unread != 0 {
		t.Fatalf("run = %+v, %v", res, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"reading":"failed"`) || !strings.Contains(string(raw), `"detail":"unparseable"`) {
		t.Errorf("an unusable answer was not recorded as such:\n%s", raw)
	}
	// A second run does not ask again: it is stored.
	d.calls = 0
	if res, _ := run.Run(context.Background(), tkCands(2)); d.calls != 0 || res.Skipped != 2 {
		t.Errorf("resume asked again: calls %d result %+v", d.calls, res)
	}
}

func TestTeacherRunStopsAtTheLimitAndResumes(t *testing.T) {
	for _, conc := range []int{1, 4} {
		var meter atomic.Int64
		d := &tkDecider{meter: &meter}
		run, path := tkRun(t, d, &meter, 3*tkCost+tkCost/2, conc)
		cands := tkCands(8)
		res, err := run.Run(context.Background(), cands)
		if err != nil || res.Stopped != TeacherStoppedAtLimit {
			t.Fatalf("conc %d: run = %+v, %v", conc, res, err)
		}
		if res.Stored != 3 || d.calls != 3 || meter.Load() > run.LimitNano {
			t.Fatalf("conc %d: stored %d, calls %d, spent %d of limit %d", conc, res.Stored, d.calls, meter.Load(), run.LimitNano)
		}
		if res.SpentNano != 3*tkCost {
			t.Errorf("conc %d: spent %d, want %d", conc, res.SpentNano, 3*tkCost)
		}
		// A new run (fresh store handle, as a new process) resumes with the rest.
		st, err := OpenTeacherStore(path)
		if err != nil || st.Len() != 3 {
			t.Fatalf("reopen: %d rows, %v", st.Len(), err)
		}
		run.Store, run.LimitNano = st, 100*tkCost
		before := d.calls
		res, err = run.Run(context.Background(), cands)
		if err != nil || res.Stopped != "" || res.Skipped != 3 || res.Stored != 5 || d.calls-before != 5 {
			t.Fatalf("conc %d: resume = %+v, %v (calls %d)", conc, res, err, d.calls-before)
		}
	}
}

func TestTeacherRunAsksDuplicateKeysOnce(t *testing.T) {
	var meter atomic.Int64
	d := &tkDecider{meter: &meter}
	run, _ := tkRun(t, d, &meter, 10*tkCost, 1)
	c := tkCands(2)
	res, err := run.Run(context.Background(), []TeacherCandidate{c[0], c[1], c[0], c[0]})
	if err != nil || d.calls != 2 || res.Considered != 2 || res.Stored != 2 {
		t.Fatalf("calls %d, result %+v, %v", d.calls, res, err)
	}
}

func TestTeacherRunHonoursCancellation(t *testing.T) {
	var meter atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	d := &tkDecider{meter: &meter, onCall: func(n int) {
		if n == 2 {
			cancel()
		}
	}}
	run, path := tkRun(t, d, &meter, 100*tkCost, 1)
	res, err := run.Run(ctx, tkCands(6))
	if err != nil || res.Stopped != "interrupted" {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if d.calls > 3 {
		t.Errorf("kept asking after cancel: %d calls", d.calls)
	}
	st, err := OpenTeacherStore(path)
	if err != nil || st.Len() != res.Stored || st.Skipped != 0 {
		t.Errorf("stored %d on disk %d (skipped %d), %v", res.Stored, st.Len(), st.Skipped, err)
	}
}

func TestTeacherRunStopsAfterUnreadStreak(t *testing.T) {
	var meter atomic.Int64
	d := &tkDecider{meter: &meter, err: errors.New("provider down")}
	run, path := tkRun(t, d, &meter, 100*tkCost, 1)
	res, err := run.Run(context.Background(), tkCands(8))
	if err != nil || res.Stored != 0 || res.Unread != TeacherStopAfter || !strings.Contains(res.Stopped, "no reading") {
		t.Fatalf("run = %+v, %v", res, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("an unread question was stored")
	}
}

func TestTeacherRunRefusesBadSettings(t *testing.T) {
	var meter atomic.Int64
	d := &tkDecider{meter: &meter}
	run, _ := tkRun(t, d, &meter, tkCost, 1)
	for name, mut := range map[string]func(*TeacherRun){
		"no limit":         func(r *TeacherRun) { r.LimitNano = 0 },
		"no concurrency":   func(r *TeacherRun) { r.Concurrency = 0 },
		"too much":         func(r *TeacherRun) { r.Concurrency = MaxTeacherConcurrency + 1 },
		"no meter":         func(r *TeacherRun) { r.Spent = nil },
		"no worst case":    func(r *TeacherRun) { r.Worst = nil },
		"no decider":       func(r *TeacherRun) { r.D = nil },
		"no model":         func(r *TeacherRun) { r.Model = "" },
		"no store":         func(r *TeacherRun) { r.Store = nil },
		"negative limit":   func(r *TeacherRun) { r.LimitNano = -1 },
		"worst past limit": func(r *TeacherRun) { r.Worst = func(decide.Question) int64 { return 1 << 40 } },
	} {
		r := run
		mut(&r)
		res, err := r.Run(context.Background(), tkCands(1))
		if name == "worst past limit" {
			if err != nil || res.Stopped != TeacherStoppedAtLimit || d.calls != 0 {
				t.Errorf("%s: result %+v err %v calls %d, want a clean stop with no call", name, res, err, d.calls)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTeacherStoreSurvivesATornTailAndHugeLines(t *testing.T) {
	var meter atomic.Int64
	d := &tkDecider{meter: &meter}
	run, path := tkRun(t, d, &meter, 100*tkCost, 1)
	if _, err := run.Run(context.Background(), tkCands(1)); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"v":1,"key":"gh:synth:torn","text_sha`) // a crash mid-row
	f.Close()
	st, err := OpenTeacherStore(path)
	if err != nil || st.Len() != 1 || st.Skipped != 1 {
		t.Fatalf("torn read: len %d skipped %d, %v", st.Len(), st.Skipped, err)
	}
	run.Store = st
	if _, err := run.Run(context.Background(), tkCands(2)); err != nil {
		t.Fatal(err)
	}
	st, err = OpenTeacherStore(path)
	if err != nil || st.Len() != 2 || st.Skipped != 1 {
		t.Fatalf("after appending past a torn tail: len %d skipped %d, %v (the new row must not be glued to the torn one)", st.Len(), st.Skipped, err)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(strings.Repeat("x", 2*maxTeacherLine) + "\n")
	f.Close()
	if st, err = OpenTeacherStore(path); err != nil || st.Len() != 2 || st.Skipped != 2 {
		t.Fatalf("oversized line: len %d skipped %d, %v", st.Len(), st.Skipped, err)
	}
}

func TestLockTeacherExcludesASecondRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), TeacherFile)
	unlock, err := LockTeacher(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockTeacher(context.Background(), path, 100*time.Millisecond); !errors.Is(err, ErrTeacherBusy) {
		t.Errorf("second lock = %v, want ErrTeacherBusy", err)
	}
	unlock()
	u2, err := LockTeacher(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("after unlock: %v", err)
	}
	u2()
}
