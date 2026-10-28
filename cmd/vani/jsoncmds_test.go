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
