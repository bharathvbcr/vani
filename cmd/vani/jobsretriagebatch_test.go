package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// `vani jobs retriage --batch`: many manual readings in one command. Every
// provider is the fake of jobsretriage_test.go.

func rtBatchEvents(t *testing.T, raw string) []retriageBatchEvent {
	t.Helper()
	var evs []retriageBatchEvent
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		var ev retriageBatchEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		evs = append(evs, ev)
	}
	if len(evs) < 2 || evs[0].Event != "start" || evs[len(evs)-1].Event != "done" || evs[len(evs)-1].Summary == nil {
		t.Fatalf("not start ... done: %s", raw)
	}
	return evs
}

func rtBatchItems(evs []retriageBatchEvent) map[string]retriageBatchEvent {
	m := map[string]retriageBatchEvent{}
	for _, ev := range evs {
		if ev.Event == "item" {
			m[ev.Key] = ev
		}
	}
	return m
}

// rtSetReading stores a current manual reading of the posting rtAppend seeds.
func rtSetReading(t *testing.T, state, key, title string) {
	t.Helper()
	rd := manualtriage.Reading{Key: posting.Key(key), Model: rtSonnet, At: jobsT0,
		Slots: map[string]string{"role_fit": "partial_fit", "seniority": "mid", "sponsorship": "not_stated", "work_mode": "remote"},
		Text:  manualtriage.TextSHA256(rtPosting(key, title)), Prompt: manualtriage.PromptSHA256("earlier")}
	if err := manualtriage.Set(manualtriage.Dir(state), rd); err != nil {
		t.Fatal(err)
	}
}

func rtStored(t *testing.T, state string) map[posting.Key]manualtriage.Reading {
	t.Helper()
	m, err := manualtriage.Read(manualtriage.Dir(state))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// rtBatchSeed seeds n triaged postings at Acme and one at another company.
func rtBatchSeed(t *testing.T, state string, n int) (*ledger.File, []string) {
	t.Helper()
	var jobs []rtJob
	var keys []string
	for i := 1; i <= n; i++ {
		k := fmt.Sprintf("gh:acme:%d", i)
		jobs = append(jobs, rtJob{key: k, title: fmt.Sprintf("Inference Engineer %d", i), fit: "adjacent", p: "0.4"})
		keys = append(keys, k)
	}
	l := rtSeed(t, state, jobs...)
	p := posting.Posting{Key: "gh:beta:1", ATS: "gh", Company: "Beta", Title: "Runtime Engineer", Location: "Remote", Description: rtDesc, URL: "https://boards.invalid/beta"}
	ann := map[string]string{"triage.role_fit": "adjacent", "triage.role_fit.reading": "answered", "triage.role_fit.backend": "d1-ultra", "triage.role_fit.p": "0.4"}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Scored, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	return l, append(keys, "gh:beta:1")
}

// The dry run says, for each key, whether it can be read and whether Sonnet
// already has, and what asking would cost both ways; it asks nothing.
func TestJobsRetriageBatchDryRunPlansAndAsksNothing(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	l, keys := rtBatchSeed(t, state, 2)
	rtAppend(t, l, rtJob{key: "gh:acme:live", title: "Not yet", status: posting.Live})
	rtAppend(t, l, rtJob{key: "gh:acme:blank", title: "Blank", desc: "   ", fit: "adjacent"})
	rtSetReading(t, state, "gh:acme:2", "Inference Engineer 2")
	before := rtStored(t, state)

	args := append([]string{"--batch", "--dry-run"}, keys...)
	args = append(args, "gh:acme:1", "gh:acme:live", "gh:acme:nope", "gh:acme:blank")
	var plan retriageBatchPlan
	if err := json.Unmarshal([]byte(rtRun(t, 0, args...)), &plan); err != nil {
		t.Fatal(err)
	}
	if p.calls() != 0 {
		t.Fatalf("a dry run asked the provider %d times", p.calls())
	}
	if len(rtStored(t, state)) != len(before) {
		t.Fatal("a dry run wrote a reading")
	}
	if !plan.Batch || !plan.DryRun || plan.Model != rtSonnet || plan.MaxKeys != maxRetriageBatch {
		t.Fatalf("%+v", plan)
	}
	// The repeated gh:acme:1 is dropped; order is the caller's.
	want := []struct{ key, skip, read string }{
		{"gh:acme:1", "", ""}, {"gh:acme:2", "", "current"}, {"gh:beta:1", "", ""},
		{"gh:acme:live", rtUntriaged, ""}, {"gh:acme:nope", rtUnknownKey, ""}, {"gh:acme:blank", rtTextGone, ""},
	}
	if len(plan.Items) != len(want) {
		t.Fatalf("items %+v", plan.Items)
	}
	for i, w := range want {
		if it := plan.Items[i]; it.Key != w.key || it.Skip != w.skip || it.Read != w.read || (w.skip != "" && it.Reason == "") {
			t.Fatalf("item %d: %+v, want %+v", i, it, w)
		}
	}
	if plan.Eligible != 3 || plan.AlreadyRead != 1 {
		t.Fatalf("eligible %d, already read %d", plan.Eligible, plan.AlreadyRead)
	}
	all, unread := plan.Estimate, plan.EstimateUnread
	if all.Postings != 3 || unread.Postings != 2 || all.MaxUSD <= unread.MaxUSD || all.TypicalUSD <= 0 || all.InputTokens <= unread.InputTokens {
		t.Fatalf("estimates: all %+v, unread %+v", all, unread)
	}
	if all.DailyCapUSD <= 0 || all.RemainingUSD == nil || all.FitAtMax == nil || *all.FitAtMax != 3 || all.CapReached {
		t.Fatalf("cap: %+v", all)
	}
}

// A tiny cap: FitAtMax counts what fits, and CapReached says not even one does.
func TestJobsRetriageBatchDryRunAgainstTheCap(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, func(c *config.Config) { c.Decide.DailyUSD.LLM = 0.000001 })
	_, keys := rtBatchSeed(t, state, 2)
	var plan retriageBatchPlan
	if err := json.Unmarshal([]byte(rtRun(t, 0, append([]string{"--batch", "--dry-run"}, keys...)...)), &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.Estimate.CapReached || plan.Estimate.FitAtMax == nil || *plan.Estimate.FitAtMax != 0 {
		t.Fatalf("%+v", plan.Estimate)
	}
}

// A run streams a start, one item per posting and a done; every reading is
// stored and ranks the posting, and each item carries before and after.
func TestJobsRetriageBatchRunStreamsAndStores(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 1000, OutputTokens: 100}}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 3)
	evs := rtBatchEvents(t, rtRun(t, 0, append([]string{"--batch", "--parallel", "2"}, keys...)...))
	if evs[0].Total != 4 || evs[0].Parallel != 2 || evs[0].Model != rtSonnet || len(evs[0].Skipped) != 0 {
		t.Fatalf("start %+v", evs[0])
	}
	items := rtBatchItems(evs)
	if len(items) != 4 || len(evs) != 6 {
		t.Fatalf("%d items in %d events", len(items), len(evs))
	}
	for _, k := range keys {
		ev := items[k]
		r := ev.Result
		if r == nil || ev.Error != "" || r.Key != k || r.Before == nil || r.After == nil || r.Cost == nil || r.Cost.USD <= 0 {
			t.Fatalf("%s: %+v", k, ev)
		}
		if r.Before.Slots["role_fit"].Value != "adjacent" || r.After.Slots["role_fit"].Value != "strong_fit" || r.After.Slots["role_fit"].Backend != "manual" {
			t.Fatalf("%s slots: %+v -> %+v", k, r.Before.Slots, r.After.Slots)
		}
		if r.After.At == nil || r.After.Model != rtSonnet {
			t.Fatalf("%s after: %+v", k, r.After)
		}
	}
	s := evs[len(evs)-1].Summary
	if s.Kept != 4 || s.Failed != 0 || s.NotAsked != 0 || s.Changed != 4 || s.Stopped != "" || s.USD <= 0 {
		t.Fatalf("summary %+v", s)
	}
	if got := rtStored(t, state); len(got) != 4 {
		t.Fatalf("%d readings stored", len(got))
	}
	if b, _ := rtFit(rtShow(t, "gh:beta:1")); b != "manual" {
		t.Fatalf("gh:beta:1 ranks on %q", b)
	}
	if p.calls() != 4 {
		t.Fatalf("%d calls", p.calls())
	}
}

// --skip-read leaves out what Sonnet has a current reading of, and says so.
func TestJobsRetriageBatchSkipRead(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 2)
	rtSetReading(t, state, "gh:acme:1", "Inference Engineer 1")
	evs := rtBatchEvents(t, rtRun(t, 0, append([]string{"--batch", "--skip-read"}, keys...)...))
	sk := evs[0].Skipped
	if evs[0].Total != 2 || len(sk) != 1 || sk[0].Key != "gh:acme:1" || sk[0].Skip != rtAlreadyRead {
		t.Fatalf("start %+v", evs[0])
	}
	if _, asked := rtBatchItems(evs)["gh:acme:1"]; asked || p.calls() != 2 {
		t.Fatalf("the read posting was asked again (%d calls)", p.calls())
	}
	if s := evs[len(evs)-1].Summary; s.Kept != 2 || s.Skipped != 1 {
		t.Fatalf("%+v", s)
	}
	if rtStored(t, state)["gh:acme:1"].Slots["role_fit"] != "partial_fit" {
		t.Fatal("the skipped reading was replaced")
	}
}

// Nothing to ask is a start and a done, not an error, and no lock or provider.
func TestJobsRetriageBatchNothingToAsk(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:live", title: "Not yet", status: posting.Live})
	evs := rtBatchEvents(t, rtRun(t, 0, "--batch", "gh:acme:live", "gh:acme:nope"))
	if len(evs) != 2 || len(evs[0].Skipped) != 2 || evs[1].Summary.Skipped != 2 || p.calls() != 0 {
		t.Fatalf("%+v", evs)
	}
}

// One posting's unusable reply is that posting's failure, with its cost; the
// batch goes on.
func TestJobsRetriageBatchOneFailureMovesOn(t *testing.T) {
	var n atomic.Int32
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 500, OutputTokens: 50}}
	p.onStream = func(context.Context) error {
		if n.Add(1) == 2 {
			p.text = "I would rather not say."
		} else {
			p.text = rtAnswer
		}
		return nil
	}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 2)
	evs := rtBatchEvents(t, rtRun(t, 0, append([]string{"--batch", "--parallel", "1"}, keys...)...))
	items := rtBatchItems(evs)
	bad := items["gh:acme:2"]
	if bad.Result != nil || bad.Code != rtUnreadable || bad.Cost == nil || bad.Cost.USD <= 0 {
		t.Fatalf("failed item %+v", bad)
	}
	s := evs[len(evs)-1].Summary
	if s.Kept != 2 || s.Failed != 1 || s.NotAsked != 0 || s.Stopped != "" {
		t.Fatalf("%+v", s)
	}
	if _, ok := rtStored(t, state)["gh:acme:2"]; ok {
		t.Fatal("an unusable reply was stored")
	}
}

// The cap reached stops the batch: what was not asked is listed, nothing more
// is asked, and the command fails (the stream still ends with done).
func TestJobsRetriageBatchStopsAtTheCap(t *testing.T) {
	var n atomic.Int32
	p := &retriageLLM{text: rtAnswer}
	p.onStream = func(context.Context) error {
		if n.Add(1) >= 2 {
			return fmt.Errorf("reserve: %w", budget.ErrExhausted)
		}
		return nil
	}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 4)
	evs := rtBatchEvents(t, rtRun(t, 1, append([]string{"--batch", "--parallel", "1"}, keys...)...))
	s := evs[len(evs)-1].Summary
	if s.Kept != 1 || s.Failed != 1 || s.NotAsked != 3 || s.Stopped != rtCapReached || s.Message == "" {
		t.Fatalf("%+v", s)
	}
	if strings.Join(s.NotAskedKeys, ",") != "gh:acme:3,gh:acme:4,gh:beta:1" {
		t.Fatalf("not asked %v", s.NotAskedKeys)
	}
	if p.calls() != 2 || len(rtStored(t, state)) != 1 {
		t.Fatalf("%d calls, %d stored", p.calls(), len(rtStored(t, state)))
	}
}

// A provider that keeps failing stops the batch after retriageBatchProviderStop
// in a row, rather than every posting waiting out its timeout.
func TestJobsRetriageBatchStopsWhenTheProviderIsDown(t *testing.T) {
	p := &retriageLLM{err: errors.New("connection refused")}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 5)
	evs := rtBatchEvents(t, rtRun(t, 1, append([]string{"--batch", "--parallel", "1"}, keys...)...))
	s := evs[len(evs)-1].Summary
	if s.Failed != retriageBatchProviderStop || s.NotAsked != 6-retriageBatchProviderStop || s.Stopped != rtProvider {
		t.Fatalf("%+v", s)
	}
	if p.calls() != retriageBatchProviderStop {
		t.Fatalf("%d calls", p.calls())
	}
	rtNoStore(t, state)
}

// Stop (SIGTERM) keeps what landed, asks nothing more and says so; the ask
// lock is released.
func TestJobsRetriageBatchCancelKeepsWhatLanded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	old := signalContext
	signalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { signalContext = old })
	var n atomic.Int32
	p := &retriageLLM{text: rtAnswer}
	p.onStream = func(c context.Context) error {
		if n.Add(1) == 3 {
			cancel()
			<-c.Done()
			return c.Err()
		}
		return nil
	}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 4)
	evs := rtBatchEvents(t, rtRun(t, 1, append([]string{"--batch", "--parallel", "1"}, keys...)...))
	s := evs[len(evs)-1].Summary
	if s.Kept != 2 || s.Failed != 0 || s.NotAsked != 3 || s.Stopped != rtCancelled {
		t.Fatalf("%+v", s)
	}
	if len(rtStored(t, state)) != 2 {
		t.Fatalf("%d stored", len(rtStored(t, state)))
	}
	signalContext = old
	p.onStream = nil
	if r := rtDecode(t, rtRun(t, 0, "gh:acme:4")); r.After == nil {
		t.Fatal("the ask lock was kept")
	}
}

// A batch while another reading is asked is refused before anything is paid.
func TestJobsRetriageBatchBusy(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 1)
	unlock, err := manualtriage.TryAskLock(manualtriage.Dir(state))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if e := rtFail(t, append([]string{"--batch"}, keys...)...); e.Code != rtBusy {
		t.Fatalf("%+v", e)
	}
	if p.calls() != 0 {
		t.Fatal("asked while busy")
	}
}

// Bad input is refused in the JSON reply (or as usage) before anything is read.
func TestJobsRetriageBatchRefusesBadInput(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtBatchSeed(t, state, 1)
	tooMany := make([]string, maxRetriageBatch+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("gh:acme:%d", i)
	}
	for name, args := range map[string][]string{
		"no keys":      {"--batch"},
		"too many":     append([]string{"--batch"}, tooMany...),
		"parallel 0":   {"--batch", "--parallel", "0", "gh:acme:1"},
		"parallel big": {"--batch", "--parallel", "9", "gh:acme:1"},
		"bad key":      {"--batch", "gh:acme:1", "bad\nkey"},
	} {
		if e := rtFail(t, args...); e.Code != rtBadRequest {
			t.Errorf("%s: %+v", name, e)
		}
	}
	mustCode(t, 2, "jobs", "retriage", "--batch", "--clear", "gh:acme:1")
	mustCode(t, 2, "jobs", "retriage", "--skip-read", "gh:acme:1")
	mustCode(t, 2, "jobs", "retriage", "--parallel", "2", "gh:acme:1")
	if p.calls() != 0 {
		t.Fatal("asked on bad input")
	}
	rtNoStore(t, state)
}

// The words for a person, without --json.
func TestJobsRetriageBatchText(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 1)
	o, _ := mustCode(t, 0, append([]string{"jobs", "retriage", "--batch", "--dry-run"}, keys...)...)
	if !strings.Contains(o, "would ask "+rtSonnet+" about 2 of 2 postings") {
		t.Fatalf("dry run: %s", o)
	}
	o, _ = mustCode(t, 0, append([]string{"jobs", "retriage", "--batch"}, keys...)...)
	for _, want := range []string{"asking " + rtSonnet + " about 2 postings", "role_fit adjacent -> strong_fit", "kept 2, failed 0", "never drops a posting"} {
		if !strings.Contains(o, want) {
			t.Fatalf("missing %q in %s", want, o)
		}
	}
}
