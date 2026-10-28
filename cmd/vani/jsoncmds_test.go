package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
)

// The JSON forms of the commands that had only text: each reply decodes into
// its type with no unknown field and nothing after it, a failure keeps the data
// a command had, and the text form is unchanged (the older tests hold it).

// decodeOne decodes stdout as exactly one JSON value of v's type.
func decodeOne(t *testing.T, stdout string, v any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		t.Fatalf("stdout %q is not one %T (%v)", stdout, v, err)
	}
}

// failureIn decodes stdout as one JSON object that is a failure: it has a
// non-empty error and code, and whatever data the command kept beside them.
func failureIn(t *testing.T, args []string, stdout string) (msg, code string) {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&m); err != nil || dec.More() {
		t.Fatalf("vani %v: stdout %q is not one JSON object (%v)", args, stdout, err)
	}
	msg, _ = m["error"].(string)
	code, _ = m["code"].(string)
	if msg == "" || code == "" {
		t.Fatalf("vani %v: %q has no error and code", args, stdout)
	}
	return msg, code
}

func TestVersionAndConfigCheckJSON(t *testing.T) {
	setup(t)
	o, _ := mustCode(t, 0, "--json", "version")
	var v struct {
		Version string `json:"version"`
	}
	if decodeOne(t, o, &v); v.Version != version {
		t.Errorf("version %+v", v)
	}
	mustCode(t, 2, "version", "--json", "extra")

	o, _ = mustCode(t, 1, "config", "check", "--json") // no config yet
	if r := onlyError(t, nil, o); r.Code != codeRuntime {
		t.Errorf("%+v", r)
	}
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	o, _ = mustCode(t, 0, "config", "check", "--json")
	var c configCheckReply
	if decodeOne(t, o, &c); c.Config != cfgPath || c.Boards != 0 || c.AllowedHosts == 0 {
		t.Errorf("config check %+v", c)
	}
}

func TestTrackJSON(t *testing.T) {
	_, tracksDir := trackSetup(t)
	o, _ := mustCode(t, 0, "--json", "track", "list")
	var l trackListReply
	decodeOne(t, o, &l)
	if l.Dir != tracksDir || len(l.Tracks) != 1 || l.Tracks[0].ID != "ml-systems" || l.Tracks[0].Label == "" || l.Error != "" {
		t.Fatalf("list %+v", l)
	}

	// The check could not run (no personal words, no bank): the track is shown
	// and the reply is a failure, as the text form exits 1.
	o, _ = mustCode(t, 1, "--json", "track", "show", "ml-systems")
	var s trackShowReply
	decodeOne(t, o, &s)
	if s.Track.ID != "ml-systems" || s.Check.OK || len(s.Check.NotRun) != 2 || s.Code != codeRuntime || !strings.Contains(s.Error, "incomplete") {
		t.Fatalf("show %+v", s)
	}

	// An unreadable track file is listed with its error, and fails the list.
	if err := os.WriteFile(filepath.Join(tracksDir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 1, "--json", "track", "list")
	l = trackListReply{}
	decodeOne(t, o, &l)
	if len(l.Tracks) != 2 || l.Code != codeRuntime || !strings.Contains(l.Error, "1 track file") {
		t.Fatalf("list with a broken file %+v", l)
	}
	for _, tr := range l.Tracks {
		if (tr.ID == "broken") != (tr.Error != "") {
			t.Errorf("row %+v", tr)
		}
	}
	mustCode(t, 2, "--json", "track", "show", "../escape")
}

func TestLookupJSON(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, args := range [][]string{{"--json", "lca", "lookup", "Acme"}, {"--json", "employer", "lookup", "Acme"}} {
		o, _ := mustCode(t, 1, args...)
		if r := onlyError(t, args, o); !strings.Contains(r.Error, "imported yet") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	csv := "CASE_NUMBER,CASE_STATUS,RECEIVED_DATE,DECISION_DATE,VISA_CLASS,EMPLOYER_NAME\n" +
		"I-1,Certified,2024-01-02,2024-01-10,H-1B,\"Acme, Inc.\"\n" +
		"I-2,Certified,2024-02-02,2024-02-10,H-1B,ACME INC\n"
	path := filepath.Join(t.TempDir(), "lca.csv")
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "lca", "import", path)
	o, _ := mustCode(t, 0, "--json", "lca", "lookup", "Acme")
	var b lcaLookupReply
	if decodeOne(t, o, &b); b.Company != "Acme" || b.Total != 2 || b.MatchedName == "" || !strings.Contains(b.Badge, "2 certified") {
		t.Errorf("lca %+v", b)
	}
	o, _ = mustCode(t, 0, "--json", "lca", "lookup", "Nobody", "Corp")
	b = lcaLookupReply{}
	if decodeOne(t, o, &b); b.Company != "Nobody Corp" || b.Total != 0 || b.Certified == nil || b.FiscalYears == nil {
		t.Errorf("lca miss %+v", b)
	}

	mustCode(t, 0, "employer", "import", writeCSV(t, "hd2024.csv", testIPEDS))
	o, _ = mustCode(t, 0, "--json", "employer", "lookup", "State University of Nowhere")
	var eb employerLookupReply
	if decodeOne(t, o, &eb); !eb.Confident || len(eb.Attributes) == 0 || !strings.Contains(eb.Badge, "cap-exempt") {
		t.Errorf("employer %+v", eb)
	}
}

func TestReasonsCheckJSON(t *testing.T) {
	cfgPath, stateDir := reasonsSetup(t, &draftLLM{text: reasonsReply(2)})
	o, _ := mustCode(t, 1, "reasons", "check", "--json")
	if r := onlyError(t, nil, o); !strings.Contains(r.Error, "vani reasons draft") {
		t.Errorf("absent: %+v", r)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCatalog(t, stateDir, cfg.TargetSummary)
	o, _ = mustCode(t, 0, "--json", "reasons", "check")
	var r reasonsCatalogReply
	if decodeOne(t, o, &r); r.Fit != 1 || r.Gap != 1 || r.Check != 1 || !strings.HasSuffix(r.Path, "reasons.json") && r.Path == "" {
		t.Errorf("check %+v", r)
	}
}

// Unconfigured, semantic status says only what it knows; the text form is the
// one TestSemanticStatus* hold.
func TestSemanticStatusJSONUnconfigured(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "--json", "semantic", "status")
	var s semanticStatus
	decodeOne(t, o, &s)
	if s.Configured || s.Profile != nil || s.Vectors != nil {
		t.Errorf("status %+v", s)
	}
}

// kbmatch status without a knowledge base is a failure, in the envelope.
func TestKBMatchStatusJSONWithoutKB(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 1, "--json", "kbmatch", "status")
	onlyError(t, nil, o)
}

func TestInitJSON(t *testing.T) {
	cfgPath, stateDir := setup(t)
	o, _ := mustCode(t, 0, "--json", "init")
	var r struct {
		Config   string `json:"config"`
		StateDir string `json:"state_dir"`
	}
	if decodeOne(t, o, &r); r.Config != cfgPath || r.StateDir != stateDir {
		t.Errorf("init %+v", r)
	}
	o, _ = mustCode(t, 1, "--json", "init")
	if e := onlyError(t, nil, o); !strings.Contains(e.Error, "refusing to overwrite") {
		t.Errorf("second init %+v", e)
	}
}

func TestWatchlistWritesJSON(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "--json", "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--allow-host")
	var b boardReply
	if decodeOne(t, o, &b); b.Board.ATS != "gh" || b.Board.Token != "acme" || b.Board.Company != "Acme" {
		t.Fatalf("add %+v", b)
	}
	o, _ = mustCode(t, 1, "--json", "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme")
	if e := onlyError(t, nil, o); !strings.Contains(e.Error, "already on the watchlist") {
		t.Errorf("duplicate %+v", e)
	}
	o, _ = mustCode(t, 0, "--json", "watchlist", "domain", "--ats", "gh", "--token", "acme", "--domain", "www.acme.test", "--allow-host")
	b = boardReply{}
	if decodeOne(t, o, &b); b.Board.Domain != "www.acme.test" {
		t.Fatalf("domain %+v", b)
	}
	o, _ = mustCode(t, 0, "--json", "watchlist", "domain", "--ats", "gh", "--token", "acme", "--domain", "")
	b = boardReply{}
	if decodeOne(t, o, &b); b.Board.Domain != "" || b.Board.Token != "acme" {
		t.Fatalf("cleared domain %+v", b)
	}
	o, _ = mustCode(t, 0, "--json", "watchlist", "remove", "--ats", "gh", "--token", "acme")
	var rm struct {
		Removed watchlistJSON `json:"removed"`
	}
	if decodeOne(t, o, &rm); rm.Removed.ATS != "gh" || rm.Removed.Token != "acme" {
		t.Fatalf("remove %+v", rm)
	}
	o, _ = mustCode(t, 1, "--json", "watchlist", "remove", "--ats", "gh", "--token", "acme")
	if e := onlyError(t, nil, o); !strings.Contains(e.Error, "not on the watchlist") {
		t.Errorf("remove again %+v", e)
	}
	if cfg, err := config.Load(cfgPath); err != nil || len(cfg.Watchlist) != 0 {
		t.Fatalf("watchlist after remove: %v %v", cfg.Watchlist, err)
	}
	// A bad flag is still a usage error, in the envelope only under the global flag.
	o, _ = mustCode(t, 2, "--json", "watchlist", "add", "--ats", "zz", "--token", "x", "--company", "X")
	onlyError(t, nil, o)
	if o, _ := mustCode(t, 2, "watchlist", "add", "--json", "--ats", "zz", "--token", "x", "--company", "X"); o != "" {
		t.Errorf("usage error under the command's own --json: stdout %q", o)
	}
}

func TestTrackUseJSON(t *testing.T) {
	cfgPath, _ := trackSetup(t)
	before, _ := os.ReadFile(cfgPath)
	// The check cannot run here (no personal words, no bank): refused, with the
	// check in the reply and the config unchanged.
	o, _ := mustCode(t, 1, "--json", "track", "use", "ml-systems")
	var r trackUseReply
	decodeOne(t, o, &r)
	if r.Saved || r.Check.OK || len(r.Check.NotRun) == 0 || !strings.Contains(r.Error, "not applied") {
		t.Fatalf("refused use %+v", r)
	}
	o, _ = mustCode(t, 0, "--json", "track", "use", "--allow-unchecked", "--dry-run", "ml-systems")
	r = trackUseReply{}
	decodeOne(t, o, &r)
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) || r.Saved || !r.DryRun || len(r.Added) != 2 || len(r.Removed) == 0 {
		t.Fatalf("dry run %+v", r)
	}
	o, _ = mustCode(t, 0, "--json", "track", "use", "--allow-unchecked", "ml-systems")
	r = trackUseReply{}
	decodeOne(t, o, &r)
	cfg, err := config.Load(cfgPath)
	if err != nil || !r.Saved || r.Error != "" || cfg.Track != "ml-systems" || r.Config != cfgPath {
		t.Fatalf("use %+v (config track %q, %v)", r, cfg.Track, err)
	}
}

func TestImportsJSON(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	csv := "CASE_NUMBER,CASE_STATUS,RECEIVED_DATE,DECISION_DATE,VISA_CLASS,EMPLOYER_NAME\n" +
		"I-1,Certified,2024-01-02,2024-01-10,H-1B,\"Acme, Inc.\"\n"
	path := filepath.Join(t.TempDir(), "lca.csv")
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "--json", "lca", "import", path)
	var l struct {
		File        string `json:"file"`
		Employers   int    `json:"employers"`
		FiscalYears []int  `json:"fiscal_years"`
	}
	if decodeOne(t, o, &l); l.File != "lca.csv" || l.Employers != 1 || len(l.FiscalYears) == 0 {
		t.Errorf("lca import %+v", l)
	}
	o, _ = mustCode(t, 0, "--json", "employer", "import", writeCSV(t, "hd2024.csv", testIPEDS))
	var em struct {
		Source struct {
			Name        string         `json:"name"`
			Kind        string         `json:"kind"`
			SHA256      string         `json:"sha256"`
			RowsRead    int            `json:"rows_read"`
			RowsCounted int            `json:"rows_counted"`
			Skipped     map[string]int `json:"skipped"`
		} `json:"source"`
		Names int `json:"names"`
		Files int `json:"files"`
	}
	if decodeOne(t, o, &em); em.Source.Kind != "ipeds_hd" || em.Source.RowsCounted != 1 || em.Files != 1 {
		t.Errorf("employer import %+v", em)
	}
	o, _ = mustCode(t, 1, "--json", "employer", "import", writeCSV(t, "bad.csv", "A,B\n1,2\n"))
	onlyError(t, nil, o)
}

func TestReasonsImportJSON(t *testing.T) {
	reasonsSetup(t, &draftLLM{})
	src := filepath.Join(t.TempDir(), "draft.json")
	if err := os.WriteFile(src, []byte(reasonsReply(2)), 0o600); err != nil {
		t.Fatal(err)
	}
	o, _ := mustCode(t, 0, "--json", "reasons", "import", src)
	var r reasonsCatalogReply
	if decodeOne(t, o, &r); r.Fit != 2 || r.Gap != 2 || r.Check != 2 {
		t.Errorf("import %+v", r)
	}
	o, _ = mustCode(t, 1, "--json", "reasons", "import", src) // exists, no --force
	onlyError(t, nil, o)
}

func TestMigrateAndExportJSONOnAnEmptyLedger(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	emptyLedger(t, stateDir)
	o, _ := mustCode(t, 0, "--json", "migrate", "rekey-fallback", "--dry-run")
	var m migrateReply
	if decodeOne(t, o, &m); !m.DryRun || m.Postings != 0 || m.Changes == nil || m.LeftAlone == nil || m.Backup != "" {
		t.Errorf("migrate %+v", m)
	}
	out := filepath.Join(t.TempDir(), "heldout.jsonl")
	o, _ = mustCode(t, 0, "--json", "export", "heldout", "--out", out)
	var h heldoutReply
	if decodeOne(t, o, &h); h.Path != out || h.Exported != 0 || h.Missing == nil || h.Error != "" {
		t.Errorf("export %+v", h)
	}
}

func TestEvalAndRelevanceJSONOnAnEmptyLedger(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "--json", "eval")
	var ev evalReply
	if decodeOne(t, o, &ev); ev.Report.Records != 0 || ev.HeldOut != nil || ev.Windows != nil {
		t.Errorf("eval %+v", ev)
	}
	o, _ = mustCode(t, 0, "--json", "relevance")
	var rel relevanceReply
	if decodeOne(t, o, &rel); rel.Relevance.Judged != 0 || rel.DropAgreement == nil && rel.DropAgreementError == "" {
		t.Errorf("relevance %+v", rel)
	}
	mustCode(t, 2, "--json", "relevance", "-k", "0")
}

func TestPrefilterJSON(t *testing.T) {
	cfgPath, stateDir := setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "--json", "prefilter")
	var r prefilterReply
	if decodeOne(t, o, &r); !r.Off || r.Report != nil {
		t.Fatalf("off %+v", r)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DropRules = []config.DropRule{{Slot: "seniority", Values: []string{"staff_plus", "manager"}, MinOpenRouterProb: 0.9},
		{Slot: "role_fit", Values: []string{"mismatch_domain"}, MinOpenRouterProb: 0.9}}
	cfg.TitleSignals.Read = []config.TitleRead{
		{Slot: "seniority", Value: "staff_plus", Match: []string{"Staff", "Principal"}, Unless: []string{"Member of Technical Staff"}},
		{Slot: "seniority", Value: "manager", Match: []string{"Manager", "Director"}},
		{Slot: "role_fit", Value: "mismatch_domain", Match: []string{"Recruiter", "Sales", "Account Executive"}, Unless: []string{"Engineer"}},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	seedPrefilter(t, stateDir)
	// The counts the text form prints ("3 of 6 live ... 10 in the ledger"), and
	// the lists only with --list.
	o, _ = mustCode(t, 0, "--json", "prefilter")
	r = prefilterReply{}
	decodeOne(t, o, &r)
	if r.Off || r.Total != 10 || r.Report.Live != 6 || r.Report.Dropped != 3 || r.Report.Rows != nil || r.Report.Fits == nil {
		t.Fatalf("prefilter %+v / %+v", r, r.Report)
	}
	o, _ = mustCode(t, 0, "--json", "prefilter", "--list")
	r = prefilterReply{}
	if decodeOne(t, o, &r); len(r.Report.Rows) != 3 {
		t.Fatalf("--list rows %+v", r.Report.Rows)
	}
}

func TestRankfitJSON(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 45)
	o, _ := mustCode(t, 0, "--json", "rankfit", "--dry-run")
	var r rankfitReply
	decodeOne(t, o, &r)
	if !r.DryRun || r.Written || r.PlattUsable["jev"] != 23 || r.WeightJudged != 0 || len(r.NotRun) != 2 || r.Error != "" {
		t.Fatalf("dry run %+v", r)
	}
	// Below the thresholds: refused, with the counts, and nothing written.
	o, _ = mustCode(t, 1, "--json", "rankfit")
	r = rankfitReply{}
	decodeOne(t, o, &r)
	if r.Written || r.Code != codeRuntime || !strings.Contains(r.Error, "nothing written") || r.Report == nil || r.Report.Labels != 45 {
		t.Fatalf("refused %+v", r)
	}
	if _, err := os.Stat(weightsPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("a refused run left a weights file: %v", err)
	}
}

func TestDigestJSON(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)
	seedLedger(t, stateDir, l)
	o, e := mustCode(t, 0, "--json", "digest", "--date", "2026-10-01")
	var r digestReply
	decodeOne(t, o, &r)
	if r.Date != "2026-10-01" || r.Path != filepath.Join(stateDir, "digests", "2026-10-01.md") || r.Error != "" {
		t.Fatalf("digest %+v", r)
	}
	if _, err := os.Stat(r.Path); err != nil {
		t.Errorf("digest file: %v", err)
	}
	if !strings.Contains(e, "digest: ") {
		t.Errorf("the text line should move to stderr under --json: %q", e)
	}
	mustCode(t, 2, "--json", "digest", "--date", "yesterday")
}

// run-once --json answers with the record the run wrote, its own output on
// stderr, and a failed run's record beside the failure.
func TestRunOnceJSON(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	o, e := mustCode(t, 0, "--json", "run-once")
	var r runOnceReply
	decodeOne(t, o, &r)
	if r.ID == "" || r.Source != "last" || r.Record.Outcome != outcomeOK || r.Record.PID != os.Getpid() || r.Error != "" {
		t.Fatalf("run-once %+v", r)
	}
	if !strings.Contains(e, "digest: ") {
		t.Errorf("the run's digest line should be on stderr: %q", e)
	}
	// The record is the one runs show --json reads.
	s, _ := mustCode(t, 0, "runs", "show", "--json", r.ID)
	var shown runShowJSON
	if decodeOne(t, s, &shown); shown.ID != r.ID || shown.Record.StartedAt != r.Record.StartedAt {
		t.Errorf("runs show %+v, run-once %+v", shown, r.runShowJSON)
	}

	setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	l.fail = true
	wire(t, l, &fakeNotifier{}, nil)
	o, _ = mustCode(t, 1, "--json", "run-once")
	r = runOnceReply{}
	decodeOne(t, o, &r)
	if r.Code != codeRuntime || !strings.Contains(r.Error, "disk full") || r.Record.Outcome == outcomeOK || r.ID == "" {
		t.Fatalf("failed run-once %+v", r)
	}
}

func TestRetriageAndRescoreDryRunJSON(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "--json", "retriage", "--dry-run")
	var rt retriageRunReply
	if decodeOne(t, o, &rt); !rt.DryRun || rt.Returned != 0 || rt.Keys == nil {
		t.Errorf("retriage %+v", rt)
	}
	o, _ = mustCode(t, 0, "--json", "retriage", "--dry-run", "--title-drops")
	rt = retriageRunReply{}
	if decodeOne(t, o, &rt); !rt.TitleDrops {
		t.Errorf("retriage --title-drops %+v", rt)
	}
	o, _ = mustCode(t, 0, "--json", "rescore", "--dry-run")
	var rs rescoreReply
	if decodeOne(t, o, &rs); !rs.DryRun || !rs.ScoringOff || rs.Limit == 0 || rs.Plan.Eligible != 0 {
		t.Errorf("rescore %+v", rs)
	}
	mustCode(t, 2, "--json", "rescore", "--limit", "-1")
}

func TestKBSyncDryRunJSON(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "")
	starts := fakeStart(t, &fakeDevPrism{})
	o, e := mustCode(t, 0, "--json", "kb", "sync", "--dry-run", "--bank", bank, "--rkb", rkb)
	var r kbSyncReply
	decodeOne(t, o, &r)
	if !r.DryRun || !r.PlannedEmpty || r.Pool == nil || r.Plan == nil || r.Plan.Actions["add"] != 2 || r.Result != nil {
		t.Fatalf("kb sync %+v", r)
	}
	if *starts != 0 {
		t.Fatal("a dry run must not start DevPrism")
	}
	if !strings.Contains(e, "live DevPrism state was not read") {
		t.Errorf("the dry run's text should be on stderr: %q", e)
	}
	if strings.Contains(o, "person@example.invalid") || strings.Contains(o, "Synthetic Person") {
		t.Fatal("the reply carries header personal data")
	}
}

// Each spending or model-backed command fails in the envelope when it cannot
// start, before it asks anything.
func TestBatchCommandsJSONCannotStart(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	for _, args := range [][]string{
		{"--json", "kbmatch", "run", "--dry-run"},
		{"--json", "teacher", "--dry-run"},
		{"--json", "semantic", "warm"},
	} {
		o, _ := mustCode(t, 1, args...)
		failureIn(t, args, o)
	}
	mustCode(t, 2, "--json", "teacher") // a real run needs --budget-usd
}

// A command refused because another vani holds the ledger says busy, so an
// agent waits rather than reading it as a fault; the text form is unchanged.
func TestLedgerLockedIsBusy(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, args := range [][]string{
		{"--json", "run-once", "--no-digest"},
		{"--json", "retriage"},
		{"--json", "rescore"},
		{"retriage", "--json"},
	} {
		o, _ := mustCode(t, 1, args...)
		if _, code := failureIn(t, args, o); code != codeBusy {
			t.Errorf("vani %v: code %q, want %q", args, code, codeBusy)
		}
	}
	if _, e := mustCode(t, 1, "retriage"); !strings.Contains(e, "locked") {
		t.Errorf("text form: %q", e)
	}
}
