package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// parseRunFlags parses args as run-once's flags.
func parseRunFlags(t *testing.T, args ...string) (runOptions, error) {
	t.Helper()
	fs := flag.NewFlagSet("run-once", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts, boards := runOptionFlags(fs)
	err := fs.Parse(args)
	opts.Boards = *boards
	return *opts, err
}

// The scope flags fold their items as `jobs list` folds the flags of the same
// names (company keys, USPS codes, areas), and a repeated flag adds to the
// list, which stays sorted and holds each item once.
func TestRunScopeFlagsFoldAsJobsList(t *testing.T) {
	o, err := parseRunFlags(t, "--company", " Acme   Corp ,initech", "--company", "ACME corp", "--state", "texas,ca", "--state", "TX",
		"--area", "El  Paso", "--or-remote")
	if err != nil {
		t.Fatal(err)
	}
	want := runOptions{Companies: []string{"acme corp", "initech"}, States: []string{"CA", "TX"}, Areas: []string{"el paso"}, OrRemote: true}
	if !slices.Equal(o.Companies, want.Companies) || !slices.Equal(o.States, want.States) || !slices.Equal(o.Areas, want.Areas) || !o.OrRemote || o.Remote {
		t.Fatalf("parsed %+v, want %+v", o, want)
	}
	if !o.scoped() || !o.tunes() || o.isZero() || o.recorded() == nil {
		t.Errorf("a scope-only run: scoped %v tunes %v zero %v", o.scoped(), o.tunes(), o.isZero())
	}
	if err := checkScope(o); err != nil {
		t.Errorf("the flags' own folding is refused: %v", err)
	}
	raw, _ := json.Marshal(o)
	if got := string(raw); got != `{"companies":["acme corp","initech"],"states":["CA","TX"],"areas":["el paso"],"or_remote":true}` {
		t.Errorf("recorded as %s", got)
	}
	// None of them: the run as the config describes it, and a plan with no Select.
	if o, _ := parseRunFlags(t); o.scoped() || !o.isZero() {
		t.Errorf("no flags: %+v", o)
	}
}

// A scope no run can apply is a usage error before the app is built: nothing
// is tuned, run or recorded.
func TestRunOnceRefusesBadScope(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	got := wireTuned(t, fixture())
	many := make([]string, maxFilterItems+1)
	for i := range many {
		many[i] = "c" + strings.Repeat("x", i+1)
	}
	for _, args := range [][]string{
		{"run-once", "--state", "USA"},
		{"run-once", "--state", "tx,,ca"},
		{"run-once", "--state", ""},
		{"run-once", "--company", ""},
		{"run-once", "--company", " , "},
		{"run-once", "--company", strings.Repeat("a", maxFilterFlagBytes+1)},
		{"run-once", "--company", strings.Join(many, ",")},
		{"run-once", "--company", "bad\xffutf8"},
		{"run-once", "--area", ""},
		{"run-once", "--area", "a,,b"},
		{"run-once", "--or-remote"},
		{"run-once", "--or-remote", "--remote", "--state", "tx"},
		{"run-once", "--list-only", "--company", "acme"},
		{"run-once", "--list-only", "--remote"},
	} {
		if code, _, stderr := do(t, args...); code != exitUsage {
			t.Errorf("vani %q: exit %d, want 2 (stderr %q)", args, code, stderr)
		}
	}
	if got.runs != 0 || len(got.tunes) != 0 {
		t.Errorf("a refused run reached the app: %+v", got)
	}
}

// The daemon reads a request's options from a file another process wrote, so
// checkScope refuses any item not in the form the flags write, rather than
// running a scope that matches nothing or more than was asked.
func TestCheckScopeRefusesUnfoldedItems(t *testing.T) {
	for name, o := range map[string]runOptions{
		"company case":    {Companies: []string{"Acme"}},
		"company space":   {Companies: []string{"acme  corp"}},
		"company empty":   {Companies: []string{""}},
		"state lower":     {States: []string{"tx"}},
		"state name":      {States: []string{"Texas"}},
		"state unknown":   {States: []string{"ZZ"}},
		"area case":       {Areas: []string{"El Paso"}},
		"area empty":      {Areas: []string{""}},
		"or-remote bare":  {OrRemote: true},
		"both remotes":    {Remote: true, OrRemote: true, States: []string{"TX"}},
		"list only":       {ListOnly: true, Companies: []string{"acme"}},
		"too many areas":  {Areas: slices.Repeat([]string{"x"}, maxAreas+1)},
		"too many states": {States: slices.Repeat([]string{"TX"}, maxScopeStates+1)},
	} {
		if err := checkScope(o); err == nil {
			t.Errorf("%s: %+v accepted", name, o)
		}
		if err := checkRunOptions(o, nil); err == nil {
			t.Errorf("%s: checkRunOptions accepted %+v", name, o)
		}
	}
	for _, o := range []runOptions{
		{},
		{Companies: []string{"acme corp"}, States: []string{"TX", "none"}, Areas: []string{"el paso", "none"}, OrRemote: true},
		{Remote: true},
		{Advance: 5, Companies: []string{"acme"}, NoDigest: true},
	} {
		if err := checkScope(o); err != nil {
			t.Errorf("%+v refused: %v", o, err)
		}
	}
}

// A served request that carries a scope runs with it; one whose scope is not
// in the folded form is dropped, and no run is made.
func TestServeRunRequestCarriesTheScope(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	mustCode(t, 0, "run-request", "--company", "Acme", "--state", "texas", "--or-remote", "--no-digest")
	got, err := serveOnce(t, stateDir, nil)
	want := runOptions{Companies: []string{"acme"}, States: []string{"TX"}, OrRemote: true, NoDigest: true}
	if err != nil || got.calls != 1 || !slices.Equal(got.opts.Companies, want.Companies) || !slices.Equal(got.opts.States, want.States) ||
		!got.opts.OrRemote || !got.opts.NoDigest {
		t.Fatalf("served %+v, %v; want options %+v", got, err, want)
	}
	writeRequestFile(t, stateDir, requestBody(t, time.Now(), runOptions{Companies: []string{"Acme"}}))
	got, err = serveOnce(t, stateDir, nil)
	if err == nil || got.calls != 0 || !strings.Contains(err.Error(), "dropped, no run made") {
		t.Errorf("an unfolded scope: served %+v, %v", got, err)
	}
}

// Under --json a scope check is the JSON envelope with exit 1 and code runtime,
// as a board check is (cmdRunRequest turns option errors into replies), so the
// app does not read it as an older vani.
func TestRunRequestScopeErrorsUnderJSON(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	announceDaemon(t, stateDir)
	o, _ := mustCode(t, exitRuntime, "run-request", "--json", "--or-remote")
	var reply errorReply
	if err := json.Unmarshal([]byte(o), &reply); err != nil || !strings.Contains(reply.Error, "--or-remote needs --state or --area") || reply.Code != codeRuntime {
		t.Errorf("stdout %q (%v)", o, err)
	}
	if requestExists(stateDir, runRequestFile) {
		t.Error("a refused request was written")
	}
}

// Only the run reads the scope: the inbox poll advances what the extension
// sent, whatever the last manual run asked, and a reset clears it.
func TestAdvancePlanScopeIsTheRunsOnly(t *testing.T) {
	w := &wired{cfg: config.Config{AdvancePerRun: 40}}
	w.tune(runOptions{Companies: []string{"acme"}, States: []string{"TX"}})
	run, inbox := w.advancePlan(kindRun, pipeline.DropPolicy{}), w.advancePlan(kindInbox, pipeline.DropPolicy{})
	if run.Select == nil || inbox.Select != nil {
		t.Fatalf("run Select set %v, inbox Select set %v", run.Select != nil, inbox.Select != nil)
	}
	at := func(company, where string, st posting.Status) ledger.Record {
		return ledger.Record{Status: st, Posting: posting.Posting{Company: company, Location: where}}
	}
	for _, c := range []struct {
		r    ledger.Record
		want bool
	}{
		{at("Acme", "Austin, TX", posting.Live), true},
		{at("  ACME ", "Dallas, Texas", posting.Scored), true},
		{at("Acme", "San Francisco, CA", posting.Live), false},
		{at("Initech", "Austin, TX", posting.Live), false},
		{at("Acme", "Austin, TX", posting.Digested), false}, // past every stage
		{at("Acme", "Austin, TX", posting.Dropped), false},
	} {
		if got := run.Select(c.r); got != c.want {
			t.Errorf("Select(%s, %q, %s) = %v, want %v", c.r.Posting.Company, c.r.Posting.Location, c.r.Status, got, c.want)
		}
	}
	w.tune(runOptions{})
	if p := w.advancePlan(kindRun, pipeline.DropPolicy{}); p.Select != nil {
		t.Error("a reset left the scope on")
	}
}

// The words a person reads for a scoped run, in `runs show` and the warnings.
func TestScopeWords(t *testing.T) {
	for _, c := range []struct {
		o    runOptions
		want string
	}{
		{runOptions{}, ""},
		{runOptions{Companies: []string{"acme", "initech"}}, "postings at acme, initech"},
		{runOptions{States: []string{"TX"}, Areas: []string{"el paso"}, OrRemote: true}, "postings in TX, el paso or remote"},
		{runOptions{Remote: true}, "remote postings"},
		{runOptions{Remote: true, Companies: []string{"acme"}, States: []string{"CA"}}, "remote postings at acme in CA"},
		{runOptions{Companies: []string{"a", "b", "c", "d", "e", "f", "g"}}, "postings at a, b, c, d, e and 2 more companies"},
	} {
		if got := scopeWords(c.o); got != c.want {
			t.Errorf("scopeWords(%+v) = %q, want %q", c.o, got, c.want)
		}
	}
	if got := optionsSummary(runOptions{Advance: 20, Companies: []string{"acme"}, NoDigest: true}); got != "advance cap 20; advancing only postings at acme; no digest" {
		t.Errorf("optionsSummary = %q", got)
	}
	// The app words US first between the cap and the scope (ManualRunOptions.summary).
	if got := optionsSummary(runOptions{Advance: 20, USFirst: true, Companies: []string{"acme"}, NoDigest: true}); got != "advance cap 20; US first; advancing only postings at acme; no digest" {
		t.Errorf("optionsSummary with us_first = %q", got)
	}
}

// An app that cannot be tuned must not run a scoped run over every posting.
func TestRunOnceScopeWithoutTuneFails(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	runs := wire(t, fixture(), &fakeNotifier{}, nil) // an App with no Tune
	_, e := mustCode(t, 1, "run-once", "--company", "acme")
	if *runs != 0 || !strings.Contains(e, "--company") {
		t.Errorf("runs %d, stderr %q", *runs, e)
	}
}

// scopeEvent is a live posting at company and where, with text enough to triage.
func scopeEvent(key posting.Key, company, where string) ledger.Event {
	ev := liveEvent(key)
	ev.Posting.Company, ev.Posting.Location = company, where
	// Text of its own, so no posting is answered from another's cached reading.
	ev.Posting.Description += " Team " + string(key) + "."
	return ev
}

// seedScopeLedger writes four live postings: Acme in Texas and in California,
// Initech in Texas, and a remote Globex one.
func seedScopeLedger(t *testing.T, stateDir string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range []ledger.Event{
		scopeEvent("gh:acme:1", "Acme", "Austin, TX"),
		scopeEvent("gh:acme:2", "Acme", "San Francisco, CA"),
		scopeEvent("gh:initech:1", "Initech", "Austin, TX"),
		scopeEvent("gh:globex:1", "Globex", "Remote"),
	} {
		if err := l.Append(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// statuses reads every posting's status from the ledger.
func statuses(t *testing.T, stateDir string) map[posting.Key]posting.Status {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	out := map[posting.Key]posting.Status{}
	for _, r := range l.All() {
		out[r.Key] = r.Status
	}
	return out
}

// pendingKeys is what `jobs list --view pending` lists for the extra flags.
func pendingKeys(t *testing.T, args ...string) []string {
	t.Helper()
	o, _ := mustCode(t, 0, append([]string{"jobs", "list", "--json", "--view", "pending"}, args...)...)
	var l jobview.List
	if err := json.Unmarshal([]byte(o), &l); err != nil {
		t.Fatalf("jobs list: %v\n%s", err, o)
	}
	var keys []string
	for _, j := range l.Jobs {
		keys = append(keys, string(j.Key))
	}
	slices.Sort(keys)
	return keys
}

// A scoped run through the CLI, the wired pipeline and a fake model triages
// exactly the postings `jobs list --view pending` lists for the same flags,
// and no other: the rest stay live for a later run. The record keeps the
// scope, and `runs show` says it.
func TestScopedRunTriagesOnlyTheScope(t *testing.T) {
	cfgPath, stateDir := setup(t)
	if err := config.Save(cfgPath, anthropicConfig(t)); err != nil {
		t.Fatal(err)
	}
	srvURL, bodies := sseMessages(t, triageReply(nil))
	t.Setenv("ANTHROPIC_BASE_URL", srvURL)
	seedScopeLedger(t, stateDir)

	scope := []string{"--company", "acme,initech", "--state", "texas"}
	preview := pendingKeys(t, scope...)
	if !slices.Equal(preview, []string{"gh:acme:1", "gh:initech:1"}) {
		t.Fatalf("jobs list --view pending %v lists %v", scope, preview)
	}
	_, stderr := mustCode(t, 0, append([]string{"run-once", "--no-digest"}, scope...)...)

	st := statuses(t, stateDir)
	var advanced []string
	for k, s := range st {
		if s != posting.Live {
			advanced = append(advanced, string(k))
		}
	}
	slices.Sort(advanced)
	if !slices.Equal(advanced, preview) {
		t.Errorf("the run advanced %v, the preview listed %v (statuses %v)\nstderr: %s", advanced, preview, st, stderr)
	}
	if len(*bodies) != len(preview) {
		t.Errorf("%d requests reached the model, want %d", len(*bodies), len(preview))
	}
	r := lastRecord(t, stateDir)
	if r.Options == nil || !slices.Equal(r.Options.Companies, []string{"acme", "initech"}) || !slices.Equal(r.Options.States, []string{"TX"}) {
		t.Errorf("record options %+v", r.Options)
	}
	if r.Advance == nil || r.Advance.Eligible != len(preview) {
		t.Errorf("record advance %+v, want %d eligible", r.Advance, len(preview))
	}
	if o, _ := mustCode(t, 0, "runs", "show"); !strings.Contains(o, "advancing only postings at acme, initech in TX") {
		t.Errorf("runs show does not say the scope:\n%s", o)
	}
	if s := statusOf(t); s.LastRun.Options == nil || !slices.Equal(s.LastRun.Options.Companies, []string{"acme", "initech"}) {
		t.Errorf("status last_run %+v", s.LastRun)
	}

	// What is left waiting is what the scope left out.
	if left := pendingKeys(t); !slices.Equal(left, []string{"gh:acme:2", "gh:globex:1"}) {
		t.Errorf("still awaiting triage: %v", left)
	}
	// --remote alone takes the remote one.
	*bodies = nil
	mustCode(t, 0, "run-once", "--no-digest", "--remote")
	if st := statuses(t, stateDir); st["gh:globex:1"] == posting.Live || st["gh:acme:2"] != posting.Live || len(*bodies) != 1 {
		t.Errorf("--remote: statuses %v, %d requests", st, len(*bodies))
	}
}

// A scope with nothing waiting in it advances nothing and asks the model
// nothing, and the run's output says so, naming a company with nothing
// waiting, so a misspelt name does not pass for "nothing to do".
func TestScopedRunWarnsWhenNothingIsInScope(t *testing.T) {
	cfgPath, stateDir := setup(t)
	if err := config.Save(cfgPath, anthropicConfig(t)); err != nil {
		t.Fatal(err)
	}
	srvURL, bodies := sseMessages(t, triageReply(nil))
	t.Setenv("ANTHROPIC_BASE_URL", srvURL)
	seedScopeLedger(t, stateDir)

	_, stderr := mustCode(t, 0, "run-once", "--no-digest", "--company", "Acme Inc")
	if len(*bodies) != 0 {
		t.Errorf("%d requests reached the model", len(*bodies))
	}
	if !strings.Contains(stderr, "advance scope (postings at acme inc): no posting waiting") {
		t.Errorf("no warning for an empty scope:\n%s", stderr)
	}
	for k, s := range statuses(t, stateDir) {
		if s != posting.Live {
			t.Errorf("%s advanced to %s", k, s)
		}
	}

	_, stderr = mustCode(t, 0, "run-once", "--no-digest", "--company", "acme,nobody", "--state", "CA")
	if !strings.Contains(stderr, `--company "nobody" has no posting waiting`) || strings.Contains(stderr, `--company "acme"`) {
		t.Errorf("warnings for a company with nothing waiting:\n%s", stderr)
	}
	if st := statuses(t, stateDir); st["gh:acme:2"] == posting.Live || st["gh:acme:1"] != posting.Live {
		t.Errorf("statuses %v", st)
	}
}
