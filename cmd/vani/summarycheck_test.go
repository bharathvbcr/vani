package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// The identifying term of these tests is synthetic (kbTestOwnWord), listed in a
// words file under t.TempDir(): no real term is read or written.

// spellings of kbTestOwnWord the matcher's fold must equal: case, an accent,
// a zero-width space inside, a fullwidth form, and a joined/separated form.
var summaryVariants = []string{
	"Roles for zzownername in ML systems",
	"Roles for ZZOWNERNAME in ML systems",
	"Roles for zzöwnername in ML systems",
	"Roles for zzown​ername in ML systems",
	"Roles for ｚｚｏｗｎｅｒｎａｍｅ in ML systems",
	"Roles for zz-owner-name in ML systems",
}

const cleanSummary = "Early-career ML systems and inference engineering roles"

// summaryConfig points the CLI at a config whose target_summary is summary and
// returns it. Words are written to the tracks directory when given.
func summaryConfig(t *testing.T, summary string, words ...string) (cfgPath, stateDir string, cfg config.Config) {
	t.Helper()
	cfgPath, stateDir = setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TargetSummary = summary
	writeConfig(t, cfgPath, cfg)
	if len(words) > 0 {
		writeKBWords(t, stateDir, words...)
	}
	return cfgPath, stateDir, cfg
}

// noTerm fails when text carries the term in any spelling the tests use, or the
// summary it was refused for.
func noTerm(t *testing.T, what, text, summary string) {
	t.Helper()
	low := strings.ToLower(text)
	if strings.Contains(low, kbTestOwnWord) || strings.Contains(low, "zz-owner") || (summary != "" && strings.Contains(text, summary)) {
		t.Errorf("%s quotes the term or the summary: %s", what, text)
	}
}

func rawStatus(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	o, _ := mustCode(t, 0, "status", "--json")
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(o), &m); err != nil {
		t.Fatalf("status --json: %v\n%s", err, o)
	}
	return m
}

type summaryStatus struct {
	OK             bool   `json:"ok"`
	Reason         string `json:"reason"`
	GenericDefault bool   `json:"generic_default"`
}

func targetSummaryStatus(t *testing.T) (summaryStatus, bool) {
	t.Helper()
	raw, ok := rawStatus(t)["target_summary"]
	if !ok {
		return summaryStatus{}, false
	}
	var s summaryStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s, true
}

func TestStatusShowsAnIdentifyingConfigSummaryAsAnErrorWithoutRefusing(t *testing.T) {
	for _, summary := range summaryVariants {
		t.Run(summary, func(t *testing.T) {
			summaryConfig(t, summary, kbTestOwnWord)
			s, ok := targetSummaryStatus(t)
			if !ok || s.OK || !strings.Contains(s.Reason, "target_summary") {
				t.Fatalf("status target_summary facet = %+v (present %v); want not ok, naming target_summary", s, ok)
			}
			noTerm(t, "status --json", s.Reason, summary)
			// The text status still runs, and says so on an error line.
			o, _ := mustCode(t, 0, "status")
			if !strings.Contains(o, "error: config target_summary") {
				t.Errorf("status has no error line for the summary:\n%s", o)
			}
			noTerm(t, "status", o, summary)
		})
	}
}

func TestStatusAcceptsACleanConfigSummary(t *testing.T) {
	summaryConfig(t, cleanSummary, kbTestOwnWord)
	s, ok := targetSummaryStatus(t)
	if !ok || !s.OK || s.GenericDefault || s.Reason != "" {
		t.Fatalf("clean summary: %+v (present %v)", s, ok)
	}
	o, _ := mustCode(t, 0, "status")
	if strings.Contains(o, "target_summary") {
		t.Errorf("status complains about a clean summary:\n%s", o)
	}
}

func TestWireAppRefusesAnIdentifyingConfigSummaryAndSaysHowToFixIt(t *testing.T) {
	for _, summary := range summaryVariants {
		t.Run(summary, func(t *testing.T) {
			_, stateDir, cfg := summaryConfig(t, summary, kbTestOwnWord)
			app, err := wireApp(cfg, stateDir, nil)
			if err == nil {
				app.close()
				t.Fatal("wireApp built the app with an identifying target_summary")
			}
			if !strings.Contains(err.Error(), "target_summary") || !strings.Contains(err.Error(), "Edit target_summary") {
				t.Errorf("the error does not name the field and the fix: %v", err)
			}
			noTerm(t, "the error", err.Error(), summary)
		})
	}
}

func TestWireAppBuildsWithACleanOrEmptySummary(t *testing.T) {
	for _, summary := range []string{cleanSummary, "", " \t​ "} {
		_, stateDir, cfg := summaryConfig(t, summary, kbTestOwnWord)
		app, err := wireApp(cfg, stateDir, nil)
		if err != nil {
			t.Fatalf("summary %q: %v", summary, err)
		}
		app.close()
	}
}

// A summary of invisible characters alone is an empty one everywhere: nothing
// is sent from it, so it needs no words file, and status names the generic
// default rather than checking a blank.
func TestBlankLookingSummaryIsEmptyEverywhere(t *testing.T) {
	for _, summary := range []string{"\u200b", "\u200b\u200d \u2060", "\u00ad\ufeff", "\u0301"} {
		_, stateDir, cfg := summaryConfig(t, summary) // no words file
		if err := checkPromptSummary(cfg, stateDir, false); err != nil {
			t.Errorf("summary %q: %v; a blank summary sends nothing", summary, err)
		}
		if s, ok := targetSummaryStatus(t); !ok || !s.OK || !s.GenericDefault {
			t.Errorf("summary %q: status %+v (present %v), want the generic default", summary, s, ok)
		}
	}
}

func TestWireAppFailsClosedWithoutAUsableWordsFile(t *testing.T) {
	// Missing, empty (comments only) and unreadable (a term past the bound).
	for name, setupWords := range map[string]func(t *testing.T, stateDir string){
		"missing":    func(*testing.T, string) {},
		"empty":      func(t *testing.T, s string) { writeKBWords(t, s, "# no terms yet") },
		"unreadable": func(t *testing.T, s string) { writeKBWords(t, s, strings.Repeat("x", 300)) },
	} {
		t.Run(name, func(t *testing.T) {
			_, stateDir, cfg := summaryConfig(t, cleanSummary)
			setupWords(t, stateDir)
			app, err := wireApp(cfg, stateDir, nil)
			if err == nil {
				app.close()
				t.Fatal("wireApp built the app although the summary could not be checked")
			}
			if !strings.Contains(err.Error(), "target_summary") {
				t.Errorf("the error does not name the field: %v", err)
			}
		})
	}
}

func TestWireAppSummaryFromATrackIsCheckedLikeOneFromTheConfig(t *testing.T) {
	// A track's summary lands in the config's target_summary when it is applied
	// (and is checked by track apply). Whatever way it got there, the config
	// value is what a prompt uses, so a value that fails the check is refused.
	_, stateDir, cfg := summaryConfig(t, "", kbTestOwnWord)
	cfg.TargetSummary = "Platform roles, as the track put it: zzownername"
	if app, err := wireApp(cfg, stateDir, nil); err == nil {
		app.close()
		t.Fatal("a summary applied from a track bypassed the check")
	}
}

func TestReloadKeepsThePreviousConfigWhenTheEditedSummaryIsIdentifying(t *testing.T) {
	e, cfgPath, start, _ := advEnv(t)
	writeKBWords(t, e.stateDir, kbTestOwnWord)
	start.TargetSummary = cleanSummary
	edited := start
	edited.TargetSummary = "Roles for ZZOWNERNAME"
	writeConfig(t, cfgPath, edited)
	reloaded := false
	app := &App{Reload: func(config.Config) { reloaded = true }}
	got, err := e.reloadConfig(app, start, nil)
	if err == nil || !strings.Contains(err.Error(), "target_summary") {
		t.Fatalf("reload: %v; want an error naming target_summary", err)
	}
	if reloaded || got.TargetSummary != cleanSummary {
		t.Errorf("the identifying summary was applied (reloaded %v, summary %q)", reloaded, got.TargetSummary)
	}
	noTerm(t, "the reload error", err.Error(), edited.TargetSummary)
}

func TestCommandsThatPromptAModelRefuseAnIdentifyingConfigSummary(t *testing.T) {
	// Each of these would otherwise reach "no llm provider": the check comes
	// first, so nothing is built from the summary.
	for _, args := range [][]string{
		{"reasons", "draft"},
		{"teacher", "--dry-run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			summaryConfig(t, "Roles for zzöwnername", kbTestOwnWord)
			code, _, stderr := do(t, args...)
			if code == 0 || !strings.Contains(stderr, "target_summary") || strings.Contains(stderr, "no llm provider") {
				t.Fatalf("%v: exit %d, stderr %s", args, code, stderr)
			}
			noTerm(t, "stderr", stderr, "")
		})
	}
}

func TestTeacherTakesTheExistingOverrideForAMissingWordsFile(t *testing.T) {
	summaryConfig(t, cleanSummary) // no words file
	code, _, stderr := do(t, "teacher", "--dry-run")
	if code == 0 || !strings.Contains(stderr, "identifying-words.txt") || strings.Contains(stderr, "no llm provider") {
		t.Fatalf("without the flag: exit %d, stderr %s", code, stderr)
	}
	code, _, stderr = do(t, "teacher", "--dry-run", "--allow-missing-identifying-words")
	if code == 0 || !strings.Contains(stderr, "no llm provider") {
		t.Fatalf("with the flag the check should pass and the run reach the provider check: exit %d, stderr %s", code, stderr)
	}
	// The flag does not lift a term the built-in list finds.
	summaryConfig(t, "Roles needing visa sponsorship")
	code, _, stderr = do(t, "teacher", "--dry-run", "--allow-missing-identifying-words")
	if code == 0 || !strings.Contains(stderr, "target_summary") {
		t.Fatalf("a built-in term passed the flag: exit %d, stderr %s", code, stderr)
	}
	noTerm(t, "stderr", stderr, "")
	if strings.Contains(strings.ToLower(stderr), "visa") {
		t.Errorf("a built-in term is quoted: %s", stderr)
	}
}

func TestStatusSaysTheGenericDefaultTargetIsInUse(t *testing.T) {
	summaryConfig(t, "", kbTestOwnWord)
	s, ok := targetSummaryStatus(t)
	if !ok || !s.OK || !s.GenericDefault || !strings.Contains(s.Reason, "target_summary") || !strings.Contains(s.Reason, "vani track apply") {
		t.Fatalf("status target_summary facet = %+v (present %v)", s, ok)
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "warning: target_summary is empty") || !strings.Contains(o, "generic default target") {
		t.Errorf("status does not warn about the generic default target:\n%s", o)
	}
}

func TestWarmReportCarriesTheGenericTargetFlag(t *testing.T) {
	// The report is what the run record and the log are made from.
	empty := semanticReport{GenericTarget: true}
	b, err := json.Marshal(empty)
	if err != nil || !strings.Contains(string(b), `"generic_target":true`) {
		t.Fatalf("report json %s (%v)", b, err)
	}
	var out strings.Builder
	e := &env{stderr: &out}
	e.logSemantic(empty)
	if !strings.Contains(out.String(), "generic default target") || !strings.Contains(out.String(), "target_summary") {
		t.Errorf("the log does not say the generic target is in use:\n%s", out.String())
	}
	out.Reset()
	e.logSemantic(semanticReport{})
	if strings.Contains(out.String(), "generic") {
		t.Errorf("the log warns without cause:\n%s", out.String())
	}
}

// writeBank writes a bank file whose mtime is age ago and returns its path.
func writeBank(t *testing.T, age time.Duration) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bank.json")
	if err := os.WriteFile(p, []byte(`{"entries":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

type kbStatus struct {
	OK      bool   `json:"ok"`
	Reason  string `json:"reason"`
	Stale   bool   `json:"stale"`
	AgeDays *int   `json:"age_days"`
}

func kbStatusOf(t *testing.T) (kbStatus, bool) {
	t.Helper()
	raw, ok := rawStatus(t)["knowledge_base"]
	if !ok {
		return kbStatus{}, false
	}
	var k kbStatus
	if err := json.Unmarshal(raw, &k); err != nil {
		t.Fatal(err)
	}
	return k, true
}

func TestStatusWarnsWhenTheBankIsOlderThanTheDefaultLimit(t *testing.T) {
	cfgPath, _, cfg := summaryConfig(t, cleanSummary, kbTestOwnWord)
	cfg.KB.Bank = writeBank(t, 45*24*time.Hour)
	writeConfig(t, cfgPath, cfg)
	k, ok := kbStatusOf(t)
	if !ok || k.OK || !k.Stale || k.AgeDays == nil || *k.AgeDays < 44 || *k.AgeDays > 46 {
		t.Fatalf("knowledge_base facet = %+v (present %v)", k, ok)
	}
	if !strings.Contains(k.Reason, "30-day") || !strings.Contains(k.Reason, "kb.stale_after_days") {
		t.Errorf("the warning does not name the limit and the knob: %s", k.Reason)
	}
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "warning: the knowledge base (kb.bank) was last written 45 days ago") {
		t.Errorf("status text has no stale-bank warning:\n%s", o)
	}
}

func TestStatusDoesNotWarnForAFreshBankAndHonoursTheConfiguredLimit(t *testing.T) {
	cfgPath, _, cfg := summaryConfig(t, cleanSummary, kbTestOwnWord)
	cfg.KB.Bank = writeBank(t, 5*24*time.Hour)
	writeConfig(t, cfgPath, cfg)
	if k, ok := kbStatusOf(t); !ok || !k.OK || k.Stale {
		t.Fatalf("a 5-day-old bank: %+v (present %v)", k, ok)
	}
	cfg.KB.Bank = writeBank(t, 45*24*time.Hour)
	cfg.KB.StaleAfterDays = 60
	writeConfig(t, cfgPath, cfg)
	if k, _ := kbStatusOf(t); !k.OK || k.Stale {
		t.Fatalf("45 days against a 60-day limit: %+v", k)
	}
	cfg.KB.StaleAfterDays = 10
	writeConfig(t, cfgPath, cfg)
	if k, _ := kbStatusOf(t); k.OK || !k.Stale {
		t.Fatalf("45 days against a 10-day limit: %+v", k)
	}
}

func TestStatusSaysWhenTheBankCannotBeStatedAndOmitsTheFacetWithoutOne(t *testing.T) {
	cfgPath, _, cfg := summaryConfig(t, cleanSummary, kbTestOwnWord)
	if _, ok := kbStatusOf(t); ok {
		t.Error("a config with no kb.bank has a knowledge_base facet")
	}
	cfg.KB.Bank = filepath.Join(t.TempDir(), "missing.json")
	writeConfig(t, cfgPath, cfg)
	k, ok := kbStatusOf(t)
	if !ok || k.OK || k.Stale || k.AgeDays != nil || !strings.Contains(k.Reason, "age is unknown") {
		t.Fatalf("a missing bank file must read as unknown, not fresh or stale: %+v (present %v)", k, ok)
	}
}

func TestConfigRejectsAnOutOfRangeStaleLimit(t *testing.T) {
	for _, d := range []int{-1, config.MaxKBStaleAfterDays + 1} {
		cfg := config.Default()
		cfg.KB.StaleAfterDays = d
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "kb.stale_after_days") {
			t.Errorf("stale_after_days %d: %v", d, err)
		}
	}
}
