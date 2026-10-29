package main

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// recIn is rec with a board location.
func recIn(key, loc string, posted time.Time, title string) ledger.Record {
	r := rec(key, crawl1, posted, title)
	r.Posting.Location = loc
	return r
}

// US first moves postings in the US ahead of the rest and keeps the usual
// order (title rank, then freshness) within each group. Off, it is the order
// without it, for every caller that does not pass it.
func TestOrderAdvanceUSFirst(t *testing.T) {
	recs := []ledger.Record{
		recIn("ab:a:1", "London, UK", days(1), ""),
		recIn("ab:a:2", "San Francisco", days(9), ""),
		recIn("ab:a:3", "Pune, MH, IN", days(2), ""),
		recIn("ab:a:4", "US, CA, Santa Clara", days(3), ""),
		recIn("ab:a:5", "Remote", days(0), ""),
		recIn("ab:a:6", "Remote - US", days(20), ""),
	}
	plain := []string{"ab:a:5", "ab:a:1", "ab:a:3", "ab:a:4", "ab:a:2", "ab:a:6"}
	if got := ordered(recs, nil); !slices.Equal(got, plain) {
		t.Fatalf("plain order %v, want %v", got, plain)
	}
	if got := orderedWith(recs, nil, withUSFirst(false)); !slices.Equal(got, plain) {
		t.Fatalf("withUSFirst(false) changed the order: %v, want %v", got, plain)
	}
	want := []string{"ab:a:4", "ab:a:2", "ab:a:6", "ab:a:5", "ab:a:1", "ab:a:3"}
	if got := orderedWith(recs, nil, withUSFirst(true)); !slices.Equal(got, want) {
		t.Fatalf("US first %v, want %v", got, want)
	}
	// Once on, a later off option does not turn it off: the run's flag and the
	// config's key are an OR.
	if got := orderedWith(recs, nil, withUSFirst(true), withUSFirst(false), nil); !slices.Equal(got, want) {
		t.Fatalf("on then off %v, want %v", got, want)
	}
}

// A title the title signals exclude still goes last, US or not, and a better
// title rank still leads within the US group.
func TestOrderAdvanceUSFirstKeepsTitleRank(t *testing.T) {
	recs := []ledger.Record{
		recIn("ab:a:1", "Austin, TX", days(1), "Sales Engineer"),
		recIn("ab:a:2", "Toronto, ON", days(1), "LLM Inference Engineer"),
		recIn("ab:a:3", "Seattle, WA", days(5), "Software Engineer"),
		recIn("ab:a:4", "New York, NY", days(9), "Inference Engineer"),
		recIn("ab:a:5", "Berlin, DE", days(1), "Sales Lead"),
	}
	w := &wired{cfg: config.Config{TitleSignals: config.TitleSignals{Include: []string{"Inference"}, Exclude: []string{"Sales"}}}}
	want := []string{"ab:a:4", "ab:a:3", "ab:a:2", "ab:a:1", "ab:a:5"}
	if got := orderedWith(recs, w.titleRank(), withUSFirst(true)); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

// A scored posting whose note is deferred waits behind every posting that can
// move, US first or not: it takes an advance slot and costs nothing.
func TestOrderAdvanceUSFirstDeferredNotesStillLast(t *testing.T) {
	deferred := recIn("ab:a:1", "San Francisco, CA", days(0), "")
	deferred.Status = posting.Scored
	deferred.Annotations = map[string]string{pipeline.AnnNotesDeferred: pipeline.DeferUnknownRelevance}
	recs := []ledger.Record{
		deferred,
		recIn("ab:a:2", "London", days(5), ""),
		recIn("ab:a:3", "Boston, MA", days(9), ""),
	}
	want := []string{"ab:a:3", "ab:a:2", "ab:a:1"}
	if got := orderedWith(recs, nil, withUSFirst(true)); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

// Adversarial locations: empty, oversized and hostile text never panic and
// never read as US, and equal keys keep ledger order (stable).
func TestOrderAdvanceUSFirstAdversarial(t *testing.T) {
	long := ""
	for range 400 {
		long += "Atlantis, Nowhere; "
	}
	recs := []ledger.Record{
		recIn("ab:a:1", "", time.Time{}, ""),
		recIn("ab:a:2", long+"Austin, TX", time.Time{}, ""),
		recIn("ab:a:3", "\x00\x01;;//,,", time.Time{}, ""),
		recIn("ab:a:4", "Remote", time.Time{}, ""),
		recIn("ab:a:5", "Denver, CO", time.Time{}, ""),
		recIn("ab:a:6", "Denver, CO", time.Time{}, ""),
	}
	want := []string{"ab:a:5", "ab:a:6", "ab:a:1", "ab:a:2", "ab:a:3", "ab:a:4"}
	if got := orderedWith(recs, nil, withUSFirst(true)); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if got := orderedWith(nil, nil, withUSFirst(true)); len(got) != 0 {
		t.Fatalf("empty todo: %v", got)
	}
}

// The run plan orders US first when the run asked (--us-first) or the config
// says so (advance_us_first); the inbox plan reads only the config, since an
// inbox poll takes no run options. A reset tune turns the run's off again.
func TestWiredAdvancePlanUSFirst(t *testing.T) {
	recs := []ledger.Record{
		recIn("ab:a:1", "London", days(1), ""),
		recIn("ab:a:2", "Chicago, IL", days(5), ""),
	}
	order := func(w *wired, kind advanceKind) []string {
		todo := slices.Clone(recs)
		w.advancePlan(kind, pipeline.DropPolicy{}).Order(todo, recs)
		return keysOf(todo)
	}
	plain, us := []string{"ab:a:1", "ab:a:2"}, []string{"ab:a:2", "ab:a:1"}

	w := &wired{cfg: config.Config{}}
	for _, c := range []struct {
		name      string
		tune      runOptions
		cfg       bool
		run, poll []string
	}{
		{"neither", runOptions{}, false, plain, plain},
		{"--us-first", runOptions{USFirst: true}, false, us, plain},
		{"advance_us_first", runOptions{}, true, us, us},
		{"both", runOptions{USFirst: true, Advance: 3}, true, us, us},
		{"reset", runOptions{}, false, plain, plain},
	} {
		w.cfg.AdvanceUSFirst = c.cfg
		w.tune(c.tune)
		if got := order(w, kindRun); !slices.Equal(got, c.run) {
			t.Errorf("%s: run order %v, want %v", c.name, got, c.run)
		}
		if got := order(w, kindInbox); !slices.Equal(got, c.poll) {
			t.Errorf("%s: inbox order %v, want %v", c.name, got, c.poll)
		}
	}
}

// --us-first reaches the app's advance phase and the run record, and a plain
// run after it resets it.
func TestRunOnceUSFirstReachesTheAppAndTheRecord(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	got := wireTuned(t, fixture())
	mustCode(t, 0, "run-once", "--us-first", "--advance", "9")
	if len(got.opts) != 1 || !got.opts[0].USFirst || got.opts[0].Advance != 9 {
		t.Fatalf("tuned %+v, want USFirst and advance 9", got.opts)
	}
	r := lastRecord(t, stateDir)
	if r.Options == nil || !r.Options.USFirst || r.Options.Advance != 9 {
		t.Fatalf("record options %+v", r.Options)
	}
	mustCode(t, 0, "run-once")
	if len(got.opts) != 2 || got.opts[1].USFirst {
		t.Fatalf("a plain run kept --us-first: %+v", got.opts)
	}
	if r := lastRecord(t, stateDir); r.Options != nil {
		t.Fatalf("a plain run recorded options %+v", r.Options)
	}
	// --us-first alone is an option the record keeps, not a plain run.
	mustCode(t, 0, "run-once", "--us-first")
	if r := lastRecord(t, stateDir); r.Options == nil || !r.Options.USFirst {
		t.Fatalf("--us-first alone recorded %+v", r.Options)
	}
}

// run-request carries --us-first to the daemon in the request file, and refuses
// it with --list-only before writing anything.
func TestRunRequestCarriesUSFirst(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	mustCode(t, exitUsage, "run-request", "--list-only", "--us-first")
	if requestExists(stateDir, runRequestFile) {
		t.Fatal("a refused request was written")
	}
	o, _ := mustCode(t, 0, "run-request", "--json", "--us-first")
	var reply runRequestReply
	if err := json.Unmarshal([]byte(o), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Options.USFirst {
		t.Errorf("reply %+v", reply)
	}
	req, err := readRunRequest(runsPath(stateDir, runRequestFile), time.Now())
	if err != nil || !req.Options.USFirst {
		t.Fatalf("written %+v, %v", req, err)
	}
}

// The JSON key is us_first, omitted when off, so an older reader of a record
// or a request sees nothing new unless the run asked for it.
func TestRunOptionsUSFirstJSON(t *testing.T) {
	b, err := json.Marshal(runOptions{USFirst: true})
	if err != nil || string(b) != `{"us_first":true}` {
		t.Fatalf("marshal %s %v", b, err)
	}
	if b, _ := json.Marshal(runOptions{Advance: 2}); string(b) != `{"advance":2}` {
		t.Fatalf("off is not omitted: %s", b)
	}
	var o runOptions
	if err := json.Unmarshal([]byte(`{"advance":3}`), &o); err != nil || o.USFirst {
		t.Fatalf("an older request reads %+v %v", o, err)
	}
	if !(runOptions{USFirst: true}).tunes() || (runOptions{USFirst: true}).isZero() {
		t.Fatal("USFirst must tune the run and count as an option")
	}
}
