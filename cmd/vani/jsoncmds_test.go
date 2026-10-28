package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
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
