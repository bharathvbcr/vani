package main

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Characterization goldens for the three views of a claim's verification
// state: claims export (claims.json), claims status and claims worksheet.
// The fixture is synthetic and covers every verdict path: a validator
// PRIMARY / LEDGER first pass, a MANUAL one, FAIL / UNBACKED / SKIP reports,
// a refuted, an unknown and a missing latest blind pass, statement passes,
// a claim that no sentence uses, a circular source and a void entry.
// The goldens were generated from the code before Assess existed; a byte of
// difference is a change in behaviour. RKB_UPDATE_GOLDEN=1 rewrites them.

const lab = "projects/lab/"

type assessFig struct {
	id, value, check, notes string
	vm                      string // validator verdict, "" = no line
	p1                      string // manual | manual-circular | manual-stale | "" = none
	p2                      string // CONFIRMED | MISMATCH | ... | stale | circular | "" = none
	p2Then                  string // a later blind pass that overrides p2
	sentences               int    // bullets that use it
	voidedEntry             bool
}

var assessFigs = []assessFig{
	{id: "clmAlpha", value: "10 ms", check: "lab.alpha", vm: "PRIMARY", p2: "CONFIRMED", sentences: 1},
	{id: "clmBravo", value: "20 ms", check: "lab.bravo", vm: "LEDGER", p2: "CONFIRMED", sentences: 1},
	{id: "clmCharlie", value: "30 ms", check: "lab.charlie", p1: "manual", p2: "CONFIRMED", sentences: 1},
	{id: "clmDelta", value: "40 ms", check: "lab.delta", vm: "FAIL", p1: "manual", p2: "CONFIRMED", sentences: 1},
	{id: "clmEcho", value: "50 ms", check: "lab.echo", vm: "UNBACKED", p2: "CONFIRMED", sentences: 1},
	{id: "clmFoxtrot", value: "60 ms", check: "lab.foxtrot", vm: "PRIMARY", p2: "MISMATCH", sentences: 1},
	{id: "clmGolf", value: "70 ms", check: "lab.golf", vm: "PRIMARY", p2: "CONFIRMED", p2Then: "SCOPE", sentences: 1},
	{id: "clmHotel", value: "80 ms", check: "lab.hotel", vm: "PRIMARY", p2: "NO_ANSWER", sentences: 1},
	{id: "clmIndia", value: "90 ms", check: "lab.india", p1: "manual-circular", p2: "CONFIRMED", sentences: 1},
	{id: "clmJuliet", value: "11 ms", check: "lab.juliet", vm: "PRIMARY", p2: "CONFIRMED", sentences: 2},
	{id: "clmKilo", value: "12 ms", check: "lab.kilo", vm: "PRIMARY", p2: "CONFIRMED", sentences: 0},
	{id: "clmLima", value: "13 ms", check: "lab.lima", vm: "PRIMARY", p2: "CONFIRMED", sentences: 1, voidedEntry: true},
	{id: "clmMike", value: "14 ms", check: "lab.mike", vm: "PRIMARY", p2: "stale", sentences: 1},
	{id: "clmNovember", value: "15 ms", check: "lab.november", vm: "PRIMARY", p2: "circular", sentences: 1},
	{id: "clmOscar", value: "16 ms", sentences: 1},
	{id: "clmPapa", value: "17 ms", check: "lab.papa", vm: "SKIP", p2: "CONFIRMED", sentences: 1},
	{id: "clmQuebec", value: "18 ms", check: "lab.quebec", vm: "PRIMARY", p2: "CONFIRMED", sentences: 1, notes: "CAUTION: measured on one phase only"},
	{id: "clmRomeo", value: "19 ms", check: "lab.romeo", p1: "manual-stale", p2: "CONFIRMED", sentences: 1},
	{id: "clmSierra", value: "21 ms", check: "lab.sierra", vm: "PRIMARY", p2: "WEIRD_VERDICT", sentences: 1},
	{id: "clmTango", value: "22 ms", check: "lab.tango", vm: "PRIMARY", p2: "QUOTE_NOT_FOUND", sentences: 1},
}

type assessStmt struct {
	id, text, state string // state: confirmed | unsupported | missing | stale | nosrc | badsrc | circular | attested | otherverdict
}

var assessStmts = []assessStmt{
	{"st-confirmed", "Wrote the lab harness in plain Go.", "confirmed"},
	{"st-unsupported", "Ran the lab harness on a cluster.", "unsupported"},
	{"st-missing", "Documented the lab harness.", "missing"},
	{"st-stale", "Tested the lab harness nightly.", "stale"},
	{"st-nosrc", "Maintained the lab harness.", "nosrc"},
	{"st-badsrc", "Packaged the lab harness.", "badsrc"},
	{"st-circular", "Reviewed the lab harness.", "circular"},
	{"st-attested", "Shipped the lab harness to a client.", "attested"},
	{"st-other", "Profiled the lab harness.", "otherverdict"},
}

const assessVMReport = `# rkb-vm-report generated=2025-01-15T12:00:00Z
[PRIMARY ] lab.alpha  value found at line 1
[LEDGER  ] lab.bravo  ledger row
[FAIL    ] lab.delta  value differs: 41 ms
[PRIMARY ] lab.delta  an earlier pass line that must not hide the FAIL
[UNBACKED] lab.echo  no artifact
[PRIMARY ] lab.foxtrot  ok
[PRIMARY ] lab.golf  ok
[PRIMARY ] lab.hotel  ok
[PRIMARY ] lab.juliet  ok
[PRIMARY ] lab.kilo  ok
[PRIMARY ] lab.lima  ok
[PRIMARY ] lab.mike  ok
[PRIMARY ] lab.november  ok
[SKIP    ] lab.papa  skipped
[PRIMARY ] lab.quebec  ok
[PRIMARY ] lab.sierra  ok
[PRIMARY ] lab.tango  ok
`

func assessBulletID(f assessFig, n int) string { return fmt.Sprintf("fig-%s-%d", strings.ToLower(f.id[3:]), n) }

func assessFixture(t *testing.T) (root string) {
	t.Helper()
	root = sampleCopy(t)
	cv := filepath.Join(root, "career", "CurriculumVitae")
	for _, f := range []string{"Claims.verify.json"} {
		if err := os.Remove(filepath.Join(cv, f)); err != nil {
			t.Fatal(err)
		}
	}
	w3aWrite(t, filepath.Join(root, lab, "src.txt"), "lab source, synthetic\n")
	w3aWrite(t, filepath.Join(root, "report.vm.txt"), assessVMReport)

	var tex strings.Builder
	tex.WriteString("% synthetic claims for the assess goldens\n")
	for _, f := range assessFigs {
		tex.WriteString("% ----\n")
		if f.notes != "" {
			tex.WriteString("% " + f.notes + "\n")
		}
		if f.check != "" {
			tex.WriteString(fmt.Sprintf("%% %ssrc.txt [%s]\n", lab, f.check))
		}
		tex.WriteString(fmt.Sprintf("\\newcommand{\\%s}{%s}\n", f.id, f.value))
	}
	w3aWrite(t, filepath.Join(cv, "core", "Claims.tex"), tex.String())

	bullets := map[string]map[string]any{}
	var figIDs []string
	for _, f := range assessFigs {
		for n := 0; n < f.sentences; n++ {
			id := assessBulletID(f, n)
			bullets[id] = map[string]any{
				"id":      id,
				"text":    fmt.Sprintf("**Measured the %s stage at {{%s}} in variant %d:** the harness replays the same inputs each run.", strings.ToLower(f.id[3:]), f.id, n),
				"need":    "synthetic",
				"sources": []string{lab + "src.txt"},
				"author":  "fixture-author",
			}
			figIDs = append(figIDs, id)
		}
	}
	var stmtIDs []string
	for _, s := range assessStmts {
		b := map[string]any{"id": s.id, "text": s.text, "need": "synthetic", "sources": []string{lab + "src.txt"}, "author": "fixture-author"}
		switch s.state {
		case "nosrc":
			delete(b, "sources")
		case "badsrc":
			b["sources"] = []string{lab + "missing.txt"}
		case "circular":
			b["sources"] = []string{"career/CurriculumVitae/core/Claims.tex"}
		case "attested":
			b["attested"] = "client work, no artifact"
			delete(b, "sources")
		}
		bullets[s.id] = b
		stmtIDs = append(stmtIDs, s.id)
	}
	bank := map[string]any{
		"headers": map[string]any{"default": map[string]any{"name": "Fixture Person", "headline": "Engineer", "contact": []string{"fixture@example.invalid"}}},
		"entries": map[string]any{
			"figs":  map[string]any{"title": "Figures lab", "dates": "2025", "stack": "Go", "lead": "A synthetic lab for claim states.", "bullets": figIDs},
			"stmts": map[string]any{"title": "Statements lab", "dates": "2025", "stack": "Go", "bullets": stmtIDs},
		},
		"bullets": bullets,
		"lines":   map[string]any{"skills": []map[string]string{{"label": "Languages", "text": "Go"}}},
	}
	writeFixtureJSON(t, filepath.Join(cv, "resume", "bank.json"), bank)
	sel := map[string]any{
		"bank": "../bank.json",
		"meta": map[string]any{"target": "fixture", "pages": 2, "paper": "letter", "output_basename": "Fixture"},
		"sections": []any{
			map[string]any{"title": "Projects", "kind": "entries", "entries": []any{map[string]string{"entry": "figs"}, map[string]string{"entry": "stmts"}}},
			map[string]any{"title": "Skills", "kind": "lines", "lines": "skills"},
		},
	}
	writeFixtureJSON(t, filepath.Join(cv, "resume", "resumes", "fixture.json"), sel)
	return root
}

func writeFixtureJSON(t *testing.T, p string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	w3aWrite(t, p, string(b))
}

const assessSel = "career/CurriculumVitae/resume/resumes/fixture.json"

// assessLog writes the verification log for the fixture, with the sentence
// and text hashes read from rkb's own worksheet.
func assessLog(t *testing.T, root string) {
	t.Helper()
	if out, code := runSample(t, root, "claims", "worksheet", assessSel, "--all", "--out", "ws-all.json"); code != 0 {
		t.Fatalf("worksheet for hashes: exit %d\n%s", code, out)
	}
	var ws Worksheet
	if err := readJSON(filepath.Join(root, "ws-all.json"), &ws); err != nil {
		t.Fatal(err)
	}
	figHash := map[string]string{} // bullet id -> sentence hash
	stmtHash := map[string]string{}
	for _, it := range ws.Items {
		if it.Kind == "figure" {
			for _, f := range assessFigs {
				for n := 0; n < f.sentences; n++ {
					if it.Claim == f.id && strings.Contains(it.Sentence, fmt.Sprintf("variant %d:", n)) {
						figHash[assessBulletID(f, n)] = it.SentenceHash
					}
				}
			}
		} else {
			stmtHash[it.Key] = it.TextHash
		}
	}
	src := filepath.Join(root, lab, "src.txt")
	sha := sha256File(src)
	if sha == "" {
		t.Fatal("fixture source not hashable")
	}
	circ := "career/CurriculumVitae/core/Claims.tex"
	const at = "2025-01-15T12:00:00Z"
	log := VerifyLog{}
	for _, f := range assessFigs {
		add := func(e LogEntry) { log[f.id] = append(log[f.id], e) }
		val := f.value
		if f.voidedEntry {
			add(LogEntry{Pass: 1, Kind: "figure", Verdict: "CONFIRMED", Method: "manual-read", By: "fixture-reader", At: at, Value: "999 ms", Found: "999 ms", SourcePath: lab + "src.txt", SourceSHA: sha})
		}
		switch f.p1 {
		case "manual":
			add(LogEntry{Pass: 1, Kind: "figure", Verdict: "CONFIRMED", Method: "manual-read", By: "fixture-reader", At: at, Value: val, Found: val, SourcePath: lab + "src.txt", SourceSHA: sha})
		case "manual-circular":
			add(LogEntry{Pass: 1, Kind: "figure", Verdict: "CONFIRMED", Method: "manual-read", By: "fixture-reader", At: at, Value: val, Found: val, SourcePath: circ, SourceSHA: sha})
		case "manual-stale":
			add(LogEntry{Pass: 1, Kind: "figure", Verdict: "CONFIRMED", Method: "manual-read", By: "fixture-reader", At: at, Value: val, Found: val, SourcePath: lab + "src.txt", SourceSHA: strings.Repeat("0", 64)})
		}
		for n := 0; n < f.sentences; n++ {
			if f.id == "clmJuliet" && n == 1 {
				continue // the second sentence has no blind pass
			}
			if f.p2 == "" {
				continue
			}
			mk := func(verdict string) LogEntry {
				e := LogEntry{Pass: 2, Kind: "figure", Verdict: Verdict(verdict), Method: "blind-rederive", By: "blind-1", At: at, Value: val, Found: val,
					Evidence: "evidence for " + verdict, SourcePath: lab + "src.txt", SourceSHA: sha, SentenceHash: figHash[assessBulletID(f, n)]}
				switch f.p2 {
				case "stale":
					e.SourceSHA = strings.Repeat("1", 64)
				case "circular":
					e.SourcePath = circ
				}
				return e
			}
			v := f.p2
			if v == "stale" || v == "circular" {
				v = "CONFIRMED"
			}
			add(mk(v))
			if f.p2Then != "" {
				add(mk(f.p2Then))
			}
		}
	}
	for _, s := range assessStmts {
		key := "stmt:" + s.id
		e := LogEntry{Pass: 2, Kind: "statement", Method: "blind-statement", By: "blind-1", At: at, TextHash: stmtHash[s.id], Verdict: "CONFIRMED", Evidence: "all assertions supported"}
		e.Sources = append(e.Sources, struct {
			Path string `json:"path"`
			SHA  string `json:"sha256"`
		}{lab + "src.txt", sha})
		switch s.state {
		case "unsupported":
			e.Verdict = "UNSUPPORTED"
		case "missing":
			continue
		case "stale":
			e.Sources[0].SHA = strings.Repeat("2", 64)
		case "otherverdict":
			e.Verdict = "NO_ANSWER"
		}
		log[key] = append(log[key], e)
	}
	// the entry lead has no pass; a stale-valued statement entry is void
	writeFixtureJSON(t, filepath.Join(root, "career", "CurriculumVitae", "Claims.verify.json"), log)
}

var (
	assessGeneratedRe = regexp.MustCompile(`"generated": "[^"]*"`)
)

func assessNormalize(root, s string) string {
	for _, r := range []string{root, evalSymlinksOr(root)} {
		s = strings.ReplaceAll(s, r, "<ROOT>")
	}
	return assessGeneratedRe.ReplaceAllString(s, `"generated": "<NOW>"`)
}

func evalSymlinksOr(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func assessCase(t *testing.T, root, name string, outFile string, args ...string) {
	t.Helper()
	out, code := runSample(t, root, args...)
	var b strings.Builder
	fmt.Fprintf(&b, "$ rkb %s\nexit: %d\n--- stdout ---\n%s", strings.Join(args, " "), code, out)
	if outFile != "" {
		data, err := os.ReadFile(filepath.Join(root, outFile))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		fmt.Fprintf(&b, "--- %s ---\n%s", outFile, data)
	}
	got := assessNormalize(root, b.String())
	golden := filepath.Join("testdata", "assess", name+".golden")
	if os.Getenv("RKB_UPDATE_GOLDEN") == "1" {
		w3aWrite(t, golden, got)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden %s (RKB_UPDATE_GOLDEN=1 writes it): %v", golden, err)
	}
	if string(want) != got {
		t.Errorf("%s differs from %s\n--- want ---\n%s\n--- got ---\n%s", name, golden, want, got)
	}
}

func TestAssessGoldens(t *testing.T) {
	root := assessFixture(t)
	assessLog(t, root)
	vm := []string{"--vm-report", "report.vm.txt"}
	cmd := func(a ...string) []string { return a }
	assessCase(t, root, "export", "claims.json", append(cmd("claims", "export", "--used-in", assessSel, "--out", "claims.json"), vm...)...)
	assessCase(t, root, "export_no_resume", "claims-bare.json", append(cmd("claims", "export", "--out", "claims-bare.json"), vm...)...)
	assessCase(t, root, "export_no_vm", "claims-novm.json", cmd("claims", "export", "--used-in", assessSel, "--out", "claims-novm.json")...)
	assessCase(t, root, "status", "", append(cmd("claims", "status", assessSel), vm...)...)
	assessCase(t, root, "status_quiet", "", append(cmd("claims", "status", assessSel, "--quiet"), vm...)...)
	assessCase(t, root, "worksheet", "ws.json", append(cmd("claims", "worksheet", assessSel, "--out", "ws.json"), vm...)...)
	assessCase(t, root, "worksheet_all", "ws-all2.json", append(cmd("claims", "worksheet", assessSel, "--all", "--out", "ws-all2.json"), vm...)...)
}
