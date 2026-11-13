package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/doctor"
)

// The Mac app reads `vani doctor --json` (macos/Sources/VaniApplyKit/Doctor.swift).
// This golden reply fills every field the reply can carry; the app's tests
// replay it in place (macos/Tests/VaniApplyKitTests/DoctorTests.swift), so a
// field added here without the app reading it fails there, not in the owner's
// hands. Rewrite with: go test ./cmd/vani -run TestDoctorReplyGolden -update

var doctorGoldenPath = filepath.Join("..", "..", "macos", "Tests", "VaniApplyKitTests", "Fixtures", "doctor", "reply.json")

func doctorGoldenReply() doctorPlanJSON {
	at := time.Date(2026, 11, 12, 14, 30, 5, 0, time.UTC)
	zero := 0
	strata := []doctor.Stratum{{Name: "title:staff_plus", Pool: 3996, Taken: 2}, {Name: "model:role_fit", Pool: 120, Taken: 1}}
	rep := doctor.Report{
		At: at, Model: "claude-opus-5-5", Through: at.Add(-2 * time.Hour), Seed: "2026-W46",
		Boards: doctor.Part{Asked: 3, Answered: 2, Error: "doctor: the reply was cut off", Raw: `{"boards":[`,
			Patterns: []doctor.Pattern{{Title: "Workday empty answers", Detail: "two wd boards read 0", Refs: []string{"wd:snapchat/snap"}, Fix: "treat Listed:0 with Total>0 as a failure"}}},
		BoardFinds: []doctor.BoardFinding{
			{Board: "wd:snapchat/snap", Verdict: doctor.VerdictSilentFailure, Confidence: "high", Evidence: "46 then 0 with no error", NextStep: "re-read the board by hand"},
			{Board: "gh:graphite", Verdict: doctor.VerdictTrulyEmpty, Confidence: "medium", Evidence: "0 every run, API agrees", NextStep: "none"},
		},
		NotExamined: []string{"gh:canva"},
		Drops: doctor.Part{Asked: 3, Answered: 2, Error: "doctor: a key was not sent", Raw: `{"drops":[{"key":"zz"}]}`, Patterns: []doctor.Pattern{{Title: "Staff titles dropped", Detail: "staff_plus no longer excluded", Refs: []string{"gh:acme:1"}, Fix: "vani jobs retriage --title-drops"}}},
		DropFinds: []doctor.DropFinding{
			{Key: "gh:acme:1", Verdict: doctor.DropWrong, Cause: "title_rule_too_broad", Why: "a staff role the target wants"},
			{Key: "lv:beta:2", Verdict: doctor.DropCorrect, Cause: "none", Why: "a sales role"},
		},
		DropsLeft: []string{"ab:gamma:3"},
		Sampled: []doctor.SampledDrop{
			{Key: "gh:acme:1", Stratum: "title:staff_plus", Company: "Acme", Title: "Staff Engineer"},
			{Key: "lv:beta:2", Stratum: "title:staff_plus", Company: "Beta", Title: "Account Executive"},
			{Key: "ab:gamma:3", Stratum: "model:role_fit", Company: "Gamma", Title: "Platform Engineer", BodyMissing: true},
		},
		Strata: strata,
		Signals: []doctor.BoardSignal{{Board: "wd:snapchat/snap", Company: "Snap", Signals: []string{doctor.SigZeroAfterNonzero, doctor.SigMassMissed},
			Runs: 3, FromLog: 1, Reads: 3, ZeroReads: 1, Failures: 1, Median: 46, Latest: &zero, Series: []int{46, 46, 0, -1},
			MaxMissed: 42, LastError: "context deadline exceeded", Details: []string{"r02.log: {Listed:0 Queries:10 Prefiltered:0}"}}},
		Logs: doctor.LogDigest{Lines: []doctor.LogLine{{Text: `workday "snapchat/snap": HTTP 503`, Count: 3, Logs: 2}},
			LogsRead: 30, LinesSeen: 41, Distinct: 12, LeftOut: 1, Withheld: 2, TooLong: 1, Unreadable: []string{"gone.log"}, TailOnlyFor: []string{"big.log"}},
		Unrecorded: 4, Recovered: 1578, RunProblems: []string{"r9.json: unreadable"}, LogMissing: 1,
	}
	return doctorPlanJSON{
		Model: "claude-opus-5-5", RunsRead: 30, RunProblems: []string{"r9.json: unreadable"}, BoardsSignaled: 3, Recovered: 1578,
		WordsPartial: true, LogMissing: 1, Drops: 3, Seed: "2026-W46", Strata: strata,
		LeftOut: map[string]int{"sponsorship": 5}, WorstUSD: 1.234567,
		Requests: map[string]string{"boards": "21 KB prompt, 16000 max tokens", "drops": "88 KB prompt, 16000 max tokens"},
		Report:   "/state/doctor/20261112T143005Z.md", Sent: true, Result: &rep,
		failure: failure{Error: "doctor: boards: doctor: the reply was cut off; drops: judged 2 of the 3 sent; the rest are not examined", Code: codeRuntime},
	}
}

func TestDoctorReplyGolden(t *testing.T) {
	raw, err := json.MarshalIndent(doctorGoldenReply(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	// Every field of the reply (and of the report inside it) is in the golden,
	// so the app's replay covers the whole contract.
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	jsonValuePaths(tree, "", have)
	want := map[string]bool{}
	jsonTypePaths(reflect.TypeFor[doctorPlanJSON](), "", want)
	var missing []string
	for p := range want {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Fatalf("the golden reply leaves fields empty: %s", strings.Join(missing, ", "))
	}
	if *updateNoticeGolden {
		if err := os.MkdirAll(filepath.Dir(doctorGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(doctorGoldenPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	stored, err := os.ReadFile(doctorGoldenPath)
	if err != nil {
		t.Fatalf("%v (go test ./cmd/vani -run TestDoctorReplyGolden -update writes it)", err)
	}
	if !bytes.Equal(stored, raw) {
		t.Errorf("%s is stale: run go test ./cmd/vani -run TestDoctorReplyGolden -update and commit it", doctorGoldenPath)
	}
}

// jsonTypePaths lists the JSON key paths t can write: struct fields by their
// json name (embedded structs flattened), slices as "[]"; a map or a time is a
// leaf.
func jsonTypePaths(t reflect.Type, prefix string, out map[string]bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == reflect.TypeFor[time.Time]():
		return
	case t.Kind() == reflect.Slice:
		jsonTypePaths(t.Elem(), prefix+"[]", out)
	case t.Kind() == reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if f.Anonymous && name == "" {
				jsonTypePaths(f.Type, prefix, out)
				continue
			}
			if !f.IsExported() || name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			p := strings.TrimPrefix(prefix+"."+name, ".")
			out[p] = true
			jsonTypePaths(f.Type, p, out)
		}
	}
}

// jsonValuePaths lists the key paths a decoded JSON value holds, in the form
// jsonTypePaths writes; a value of false, 0 or "" counts as present only when
// it is not the zero an omitted field would also read as, so an empty field is
// not mistaken for a covered one.
func jsonValuePaths(v any, prefix string, out map[string]bool) {
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			p := strings.TrimPrefix(prefix+"."+k, ".")
			switch s := sub.(type) {
			case bool:
				if !s {
					continue
				}
			case float64:
				if s == 0 && k != "latest_listed" {
					continue
				}
			case string:
				if s == "" {
					continue
				}
			case []any:
				if len(s) == 0 {
					continue
				}
			case nil:
				continue
			}
			out[p] = true
			jsonValuePaths(sub, p, out)
		}
	case []any:
		for _, sub := range x {
			jsonValuePaths(sub, prefix+"[]", out)
		}
	}
}

// --last prints the newest report written, with the sampled drops' titles,
// and asks nothing; with none written it is an empty reply, and a newest
// report that cannot be read fails rather than showing an older one.
func TestDoctorLast(t *testing.T) {
	state, _ := doctorSetup(t)
	built := doctorSeams(t, &doctorFake{boards: "echo", drops: "echo"}, 0)
	out, _ := mustCode(t, 0, "doctor", "--last", "--json")
	if strings.TrimSpace(out) != `{}` {
		t.Fatalf("no report yet: %q", out)
	}
	mustCode(t, 2, "doctor", "--last", "--dry-run")
	mustCode(t, 2, "doctor", "--last", "--budget-usd", "5")

	out, _ = mustCode(t, 0, "doctor", "--json", "--budget-usd", "5")
	var run doctorPlanJSON
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatal(err)
	}
	if !run.Sent || run.Result == nil || run.Report == "" || len(run.Result.Sampled) == 0 || run.Seed == "" {
		t.Fatalf("real run reply: %+v", run)
	}
	for _, s := range run.Result.Sampled {
		if s.Title == "" || s.Stratum == "" {
			t.Fatalf("a sampled drop without its title or stratum: %+v", s)
		}
	}
	*built = false
	out, _ = mustCode(t, 0, "doctor", "--last", "--json")
	var last doctorLastJSON
	if err := json.Unmarshal([]byte(out), &last); err != nil {
		t.Fatal(err)
	}
	if *built || last.Report != run.Report || last.Result == nil || !reflect.DeepEqual(last.Result.Sampled, run.Result.Sampled) {
		t.Fatalf("--last %+v (built %v), run %q", last, *built, run.Report)
	}
	text, _ := mustCode(t, 0, "doctor", "--last")
	if !strings.Contains(text, "the newest report is") || !strings.Contains(text, "## Dropped postings") {
		t.Fatalf("text:\n%s", text)
	}

	// A newer report that cannot be read is an error, not the older one.
	bad := filepath.Join(state, doctorReportDir, "29991231T235959Z.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, e := mustCode(t, 1, "doctor", "--last"); !strings.Contains(e, "29991231T235959Z.json is unreadable") {
		t.Fatalf("stderr %q", e)
	}
	// A file that is not a report name is not read.
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, doctorReportDir, "zzz.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "doctor", "--last")
}

// A refusal before anything is sent says so: sent is false.
func TestDoctorRefusalReplySaysNothingWasSent(t *testing.T) {
	doctorSetup(t)
	doctorSeams(t, &doctorFake{}, 0)
	out, _ := mustCode(t, 1, "doctor", "--json", "--budget-usd", "0.01")
	var reply doctorPlanJSON
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Sent || reply.Result != nil || !strings.Contains(reply.Error, "over --budget-usd") || reply.WorstUSD <= 0 {
		t.Fatalf("%+v", reply)
	}
}
