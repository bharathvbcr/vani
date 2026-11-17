package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// rtFillStore writes a store of n readings of keys the ledger never held
// (gh:filler:<i>) plus extra, one line each. It writes the file directly: n
// Sets would each replay the whole store.
func rtFillStore(t *testing.T, state string, n int, extra ...manualtriage.Reading) {
	t.Helper()
	type line struct {
		V      int               `json:"v"`
		At     time.Time         `json:"at"`
		Key    posting.Key       `json:"key"`
		Op     string            `json:"op"`
		Slots  map[string]string `json:"slots"`
		Model  string            `json:"model"`
		Text   string            `json:"text"`
		Prompt string            `json:"prompt"`
	}
	slots := map[string]string{"role_fit": "partial_fit", "seniority": "mid", "sponsorship": "not_stated", "work_mode": "remote"}
	h := strings.Repeat("a", 64)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for i := range n {
		if err := enc.Encode(line{V: 1, At: jobsT0, Key: posting.Key(fmt.Sprintf("gh:filler:%d", i)), Op: "set", Slots: slots, Model: rtSonnet, Text: h, Prompt: h}); err != nil {
			t.Fatal(err)
		}
	}
	for _, rd := range extra {
		if err := enc.Encode(line{V: 1, At: rd.At.UTC(), Key: rd.Key, Op: "set", Slots: rd.Slots, Model: rd.Model, Text: rd.Text, Prompt: rd.Prompt}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(manualtriage.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rtStoreFile(state), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if m := rtStored(t, state); len(m) != n+len(extra) {
		t.Fatalf("filled store holds %d readings, want %d", len(m), n+len(extra))
	}
}

// rtReadingOf is a reading of the posting rtAppend seeds under key and title,
// made when its description was desc.
func rtReadingOf(key, title, desc string) manualtriage.Reading {
	p := rtPosting(key, title)
	p.Description = desc
	return manualtriage.Reading{Key: posting.Key(key), Model: rtSonnet, At: jobsT0,
		Slots: map[string]string{"role_fit": "partial_fit", "seniority": "mid", "sponsorship": "not_stated", "work_mode": "remote"},
		Text:  manualtriage.TextSHA256(p), Prompt: manualtriage.PromptSHA256("earlier")}
}

// A store full of readings still in use refuses a new posting before any model
// is asked: nothing is paid, nothing is written, and the dry run says so first.
// (Until 2026-11-17 the model was paid and the save then failed with "store".)
// A posting the store already holds a reading of is still read again.
func TestJobsRetriageFullStoreIsRefusedBeforePaying(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 900, OutputTokens: 40}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state,
		rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"},
		rtJob{key: "gh:acme:2", title: "Runtime Engineer", fit: "adjacent", p: "0.9"})
	rtFillStore(t, state, manualtriage.MaxEntries-1, rtReadingOf("gh:acme:2", "Runtime Engineer", rtDesc))
	before, err := os.ReadFile(rtStoreFile(state))
	if err != nil {
		t.Fatal(err)
	}

	if d := rtDecode(t, rtRun(t, 0, "--dry-run", "gh:acme:1")); !d.StoreFull {
		t.Fatalf("the dry run did not say the store is full: %+v", d)
	}
	e := rtFail(t, "gh:acme:1")
	if e.Code != rtStoreFull || e.Cost != nil || !strings.Contains(e.Error, "nothing was paid") {
		t.Fatalf("error %+v", e)
	}
	if p.calls() != 0 {
		t.Fatalf("the model was asked %d times for a reading the store had no room for", p.calls())
	}
	after, err := os.ReadFile(rtStoreFile(state))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a refused reading changed the store")
	}

	// A reading the store already holds takes no new place.
	if d := rtDecode(t, rtRun(t, 0, "--dry-run", "gh:acme:2")); d.StoreFull {
		t.Fatal("the dry run calls a re-read of a stored posting full")
	}
	r := rtDecode(t, rtRun(t, 0, "gh:acme:2"))
	if r.Cost == nil || p.calls() != 1 {
		t.Fatalf("re-read: %+v, calls %d", r, p.calls())
	}
	if m := rtStored(t, state); len(m) != manualtriage.MaxEntries || !m["gh:acme:2"].At.After(jobsT0) {
		t.Fatalf("store after the re-read: %d readings, gh:acme:2 at %v", len(m), m["gh:acme:2"].At)
	}
}

// No paid reading is removed to make room, not even one of a posting whose text
// has changed since (ranking ignores it, but the ledger's events still hold the
// text it was asked about): a full store refuses the new posting unpaid, and the
// stale reading stays.
func TestJobsRetriageFullStoreKeepsReadingsOfChangedText(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state,
		rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"},
		rtJob{key: "gh:acme:2", title: "Runtime Engineer", fit: "adjacent", p: "0.9"})
	changed := rtReadingOf("gh:acme:2", "Runtime Engineer", "The role as it read before the board rewrote it.")
	rtFillStore(t, state, manualtriage.MaxEntries-1, changed)
	if j := rtShow(t, "gh:acme:2"); j.ManualTriage == nil || j.ManualTriage.Current {
		t.Fatalf("the seeded reading is not stale: %+v", j.ManualTriage)
	}
	before, err := os.ReadFile(rtStoreFile(state))
	if err != nil {
		t.Fatal(err)
	}
	if d := rtDecode(t, rtRun(t, 0, "--dry-run", "gh:acme:1")); !d.StoreFull {
		t.Fatal("the dry run counted a stale reading as room")
	}
	if e := rtFail(t, "gh:acme:1"); e.Code != rtStoreFull || e.Cost != nil || p.calls() != 0 {
		t.Fatalf("error %+v, calls %d", e, p.calls())
	}
	if after, _ := os.ReadFile(rtStoreFile(state)); !bytes.Equal(before, after) {
		t.Fatal("the store changed")
	}
}

// A dry run on a store that does not read says so, with the code the run would
// give, instead of an estimate the run could not keep (before 2026-11-17 it
// answered the estimate).
func TestJobsRetriageDryRunOnAnUnreadableStoreIsTheStoreError(t *testing.T) {
	p := &retriageLLM{text: rtAnswer}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.9"})
	if err := os.MkdirAll(manualtriage.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rtStoreFile(state), []byte("{broken}\n{\"v\":1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := rtFail(t, "--dry-run", "gh:acme:1"); e.Code != rtStore || e.Cost != nil || p.calls() != 0 {
		t.Fatalf("error %+v, calls %d", e, p.calls())
	}
}

// A batch finds its room before it pays: the dry run says how many of the
// postings with no reading fit, and a run asks only those, then stops unasked
// and unpaid at the first that finds none.
func TestJobsRetriageBatchStopsWhereTheStoreIsFull(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 1000, OutputTokens: 100}}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 3) // gh:acme:1..3 and gh:beta:1
	rtFillStore(t, state, manualtriage.MaxEntries-2, rtReadingOf("gh:acme:2", "Inference Engineer 2", rtDesc))

	var plan retriageBatchPlan
	if err := json.Unmarshal([]byte(rtRun(t, 0, append([]string{"--batch", "--dry-run"}, keys...)...)), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.New != 3 || plan.StoreRoom != 1 {
		t.Fatalf("plan: new %d, store room %d; want 3 and 1", plan.New, plan.StoreRoom)
	}

	raw := rtRun(t, 1, append([]string{"--batch", "--parallel", "1"}, keys...)...)
	evs := rtBatchEvents(t, raw)
	sum := evs[len(evs)-1].Summary
	// gh:acme:1 takes the one place, gh:acme:2 needs none, gh:acme:3 finds none.
	if sum.Kept != 2 || sum.Failed != 0 || sum.NotAsked != 2 || sum.Stopped != rtStoreFull || !strings.Contains(sum.Message, "nothing was paid") {
		t.Fatalf("summary %+v", sum)
	}
	if strings.Join(sum.NotAskedKeys, ",") != "gh:acme:3,gh:beta:1" {
		t.Fatalf("not asked %v", sum.NotAskedKeys)
	}
	if p.calls() != 2 {
		t.Fatalf("%d calls, want the 2 the store had room for", p.calls())
	}
	m := rtStored(t, state)
	if len(m) != manualtriage.MaxEntries {
		t.Fatalf("store holds %d readings, want %d", len(m), manualtriage.MaxEntries)
	}
	for _, k := range []posting.Key{"gh:acme:3", "gh:beta:1"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s was stored past the bound", k)
		}
	}
}

// Reading again a posting the store holds a reading of, stale or not, takes no
// new place, so a full store still lets the batch read it, and stops unpaid at
// the posting with no reading.
func TestJobsRetriageBatchRereadsInAFullStore(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 1000, OutputTokens: 100}}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state,
		rtJob{key: "gh:acme:1", title: "Inference Engineer", fit: "adjacent", p: "0.4"},
		rtJob{key: "gh:acme:2", title: "Runtime Engineer", fit: "adjacent", p: "0.4"})
	stale := rtReadingOf("gh:acme:2", "Runtime Engineer", "The role as it read before the board rewrote it.")
	rtFillStore(t, state, manualtriage.MaxEntries-1, stale)

	var plan retriageBatchPlan
	if err := json.Unmarshal([]byte(rtRun(t, 0, "--batch", "--dry-run", "gh:acme:1", "gh:acme:2")), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.New != 1 || plan.StoreRoom != 0 {
		t.Fatalf("plan: new %d, store room %d; want 1 and 0", plan.New, plan.StoreRoom)
	}
	raw := rtRun(t, 1, "--batch", "--parallel", "1", "gh:acme:2", "gh:acme:1")
	evs := rtBatchEvents(t, raw)
	for _, ev := range rtBatchItems(evs) {
		if ev.Result == nil {
			t.Errorf("%s failed (%s): %s", ev.Key, ev.Code, ev.Error)
		}
	}
	sum := evs[len(evs)-1].Summary
	if sum.Kept != 1 || sum.NotAsked != 1 || sum.Stopped != rtStoreFull || p.calls() != 1 {
		t.Fatalf("summary %+v, calls %d", sum, p.calls())
	}
	m := rtStored(t, state)
	if !m["gh:acme:2"].At.After(jobsT0) || len(m) != manualtriage.MaxEntries {
		t.Fatalf("gh:acme:2 at %v, %d readings", m["gh:acme:2"].At, len(m))
	}
}

// A posting whose reading fails gives its place back, so a later posting can
// take it.
func TestJobsRetriageBatchFailedReadingFreesItsPlace(t *testing.T) {
	p := &retriageLLM{text: "not the four readings", usage: llm.Usage{InputTokens: 10, OutputTokens: 1}}
	_, state := rtSetup(t, p, nil)
	_, keys := rtBatchSeed(t, state, 2)
	rtFillStore(t, state, manualtriage.MaxEntries-1)
	raw := rtRun(t, 0, append([]string{"--batch", "--parallel", "1"}, keys...)...)
	sum := rtBatchEvents(t, raw)[len(rtBatchEvents(t, raw))-1].Summary
	if sum.Failed != 3 || sum.NotAsked != 0 || sum.Stopped != "" || p.calls() != 3 {
		t.Fatalf("summary %+v, calls %d", sum, p.calls())
	}
}
