package suggest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

func readOutcomes(t *testing.T, dir string) ([]Outcome, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, OutcomeDir, OutcomeFile))
	if err != nil {
		t.Fatal(err)
	}
	var out []Outcome
	bad := 0
	for _, ln := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var o Outcome
		if err := json.Unmarshal(ln, &o); err != nil {
			bad++
			continue
		}
		out = append(out, o)
	}
	return out, bad
}

// No label, draft, error message, id, company or title can reach the log, from
// either command, even when a result carries them in every string it has.
func TestNoQuestionDraftOrNameReachesTheOutcomeLog(t *testing.T) {
	const secret = "ZEBRA-SECRET"
	dir := t.TempDir()
	d := &fakeDecider{byLabel: map[string]decide.SlotReading{
		secret + " essay label": answered(fieldtriage.Essay),
		secret + " fact label":  answered(fieldtriage.Fact),
	}}
	dr := &scriptDrafter{fn: func(n int, r draft.Request) (draft.Result, error) {
		if n == 1 {
			return draft.Result{}, &draft.Error{Code: draft.CodeUngrounded, Kind: draft.KindFigure, Err: fmt.Errorf("the draft says %q", secret+" 99%")}
		}
		return draft.Result{Draft: secret + " draft text", Model: "claude-test", Turns: 1}, nil
	}}
	rec := func(o Outcome) {
		if err := AppendOutcome(dir, o); err != nil {
			t.Error(err)
		}
	}
	job := draft.Job{Company: secret + " Corp", Title: secret + " Title", Description: strings.Repeat(secret+" description. ", 10)}
	fields := []fieldtriage.Field{
		{ID: secret + "-id-1", Label: secret + " essay label", Kind: fieldtriage.KindLong},
		{ID: secret + "-id-2", Label: secret + " essay label", Kind: fieldtriage.KindLong},
		{ID: secret + "-id-3", Label: secret + " fact label", Kind: fieldtriage.KindText},
		{ID: secret + "-id-4", Label: "Visa " + secret, Kind: fieldtriage.KindText},
	}
	// The ids of two fields share a label, so one is drafted after the first fails.
	if _, err := (Engine{D: d, Drafter: dr, Record: rec}).Run(context.Background(), Request{Job: job, Fields: fields}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rec(DraftOutcome(start, draft.Result{Draft: secret + " draft", Model: "claude-test", Turns: 2, Repaired: true}, nil, ""))
	rec(DraftOutcome(start, draft.Result{}, &draft.Error{Code: draft.CodeUngrounded, Kind: draft.KindYears, Turns: 2, Err: errors.New(secret + " 5 years")}, draft.CodeUngrounded))
	rec(DraftOutcome(start, draft.Result{}, &draft.Error{Code: draft.CodeNotDrafted, Category: fieldtriage.WorkAuth, Err: errors.New(secret)}, draft.CodeNotDrafted))
	rec(FailureOutcome(OpSuggest, start, "config"))

	raw, err := os.ReadFile(filepath.Join(dir, OutcomeDir, OutcomeFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{secret, "ZEBRA", "Corp", "Title", "description", "99%", "5 years", "essay label", "id-1"} {
		if bytes.Contains(raw, []byte(leak)) {
			t.Errorf("the outcome log holds %q:\n%s", leak, raw)
		}
	}
	outs, bad := readOutcomes(t, dir)
	if bad != 0 || len(outs) != 5 {
		t.Fatalf("%d records, %d unreadable", len(outs), bad)
	}
	if o := outs[0]; o.V != 1 || o.Op != OpSuggest || o.Attempts != 2 || o.Drafted != 1 || len(o.Fields) != 4 {
		t.Errorf("suggest record = %+v", o)
	}
	if f := outs[2].Fields[0]; f.Status != Failed || f.Code != draft.CodeUngrounded || f.Kind != draft.KindYears || f.Turns != 2 {
		t.Errorf("failed draft record = %+v", f)
	}
	if f := outs[3].Fields[0]; f.Status != Manual || f.Category != fieldtriage.WorkAuth {
		t.Errorf("refused draft record = %+v", f)
	}
}

// Whatever string a result carries, a record holds only a short identifier.
func TestEveryRecordedStringIsAShortIdentifier(t *testing.T) {
	hostile := []string{"Acme Corp, John Smith", "Why do you want to work here?", strings.Repeat("a", maxTokenBytes+1), "UPPER", "a\nb", "ünï", "x\"y"}
	for _, h := range hostile {
		if got := clean(h); got != "invalid" {
			t.Errorf("clean(%q) = %q", h, got)
		}
		line, err := Outcome{Op: h, Code: h, Fields: []FieldOutcome{{Category: h, Status: h, Code: h, Kind: h, Backend: h, Model: h}}}.line()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(line), "Acme") || strings.Contains(string(line), "work here") || bytes.Count(line, []byte{'\n'}) != 1 {
			t.Errorf("hostile string reached the line: %q", line)
		}
	}
	for _, ok := range []string{"", "essay", "budget_exhausted", "claude-opus-5-5", "openrouter", "anthropic/claude-x", "a.b:c"} {
		if got := clean(ok); got != ok {
			t.Errorf("clean(%q) = %q", ok, got)
		}
	}
	// Structurally: the record types hold numbers, bools and strings, and the
	// one slice is of FieldOutcome, which holds the same. No map, no interface.
	var check func(reflect.Type)
	check = func(ty reflect.Type) {
		for i := range ty.NumField() {
			switch k := ty.Field(i).Type; k.Kind() {
			case reflect.String, reflect.Int, reflect.Int64, reflect.Bool:
			case reflect.Slice:
				if k.Elem() != reflect.TypeOf(FieldOutcome{}) {
					t.Errorf("%s.%s is a slice of %s", ty.Name(), ty.Field(i).Name, k.Elem())
				}
			default:
				t.Errorf("%s.%s has kind %s", ty.Name(), ty.Field(i).Name, k.Kind())
			}
		}
	}
	check(reflect.TypeOf(Outcome{}))
	check(reflect.TypeOf(FieldOutcome{}))
}

func TestOutcomeLogIsBoundedAndRotates(t *testing.T) {
	dir := t.TempDir()
	o := FailureOutcome(OpSuggest, time.Now(), "provider")
	line, _ := o.line()
	n := MaxOutcomeBytes/len(line) + 50
	for range n {
		if err := AppendOutcome(dir, o); err != nil {
			t.Fatal(err)
		}
	}
	live, err := os.Stat(filepath.Join(dir, OutcomeDir, OutcomeFile))
	if err != nil {
		t.Fatal(err)
	}
	if live.Size() > MaxOutcomeBytes {
		t.Errorf("live log is %d bytes, over %d", live.Size(), MaxOutcomeBytes)
	}
	old, err := os.Stat(filepath.Join(dir, OutcomeDir, OutcomeRotated))
	if err != nil || old.Size() == 0 || old.Size() > MaxOutcomeBytes {
		t.Errorf("rotated log: %v, %v", old, err)
	}
	outs, bad := readOutcomes(t, dir)
	if bad != 0 || len(outs) == 0 {
		t.Errorf("%d readable, %d broken after rotation", len(outs), bad)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, OutcomeDir))
	var names []string
	for _, e := range entries {
		if e.Name() != OutcomeFile+".lock" { // the lock file is not a generation
			names = append(names, e.Name())
		}
	}
	if len(names) != 2 {
		t.Errorf("%v in the outcome dir, want the live log and one generation", names)
	}
}

func TestOutcomeAppendSurvivesATornTailAndConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	if err := AppendOutcome(dir, FailureOutcome(OpDraft, time.Now(), "provider")); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dir, OutcomeDir, OutcomeFile)
	f, _ := os.OpenFile(live, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"v":1,"op":"dra`) // a crash mid-line
	_ = f.Close()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := AppendOutcome(dir, FailureOutcome(OpSuggest, time.Now(), "timeout")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	outs, bad := readOutcomes(t, dir)
	if len(outs) != 17 || bad != 1 {
		t.Errorf("%d records and %d broken lines, want 17 and exactly the torn one", len(outs), bad)
	}
}

func TestOutcomeAppendRefusesWhatItCannotSafelyWrite(t *testing.T) {
	if err := AppendOutcome("", FailureOutcome(OpDraft, time.Now(), "x")); err == nil {
		t.Error("an empty state dir would write into the working directory")
	}
	dir := t.TempDir()
	// A symlink at the leaf is not followed.
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, OutcomeDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, OutcomeDir, OutcomeFile)); err != nil {
		t.Skip("no symlinks:", err)
	}
	if err := AppendOutcome(dir, FailureOutcome(OpDraft, time.Now(), "x")); err == nil {
		t.Error("appended through a symlink")
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Errorf("the symlink target was written: %q", b)
	}
	// Too many fields, and a line over the bound, are refused, not truncated.
	if err := AppendOutcome(t.TempDir(), Outcome{Op: OpSuggest, Fields: make([]FieldOutcome, MaxFields+1)}); err == nil {
		t.Error("an outcome over MaxFields was written")
	}
}

func TestAFullPageOutcomeFitsTheLineBound(t *testing.T) {
	fs := make([]FieldOutcome, MaxFields)
	for i := range fs {
		s := strings.Repeat("a", maxTokenBytes)
		fs[i] = FieldOutcome{Category: s, Status: s, Code: s, Kind: s, AnswerKind: s, Backend: s, Model: s, Turns: 2, Repaired: true, DraftMS: 1 << 40}
	}
	if line, err := (Outcome{Op: OpSuggest, Fields: fs}).line(); err != nil {
		t.Errorf("a page of %d maximal fields is %d bytes: %v", MaxFields, len(line), err)
	}
}
