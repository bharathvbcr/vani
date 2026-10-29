package fmbackend

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/decide"
)

var fakeHelper string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fakefm")
	if err != nil {
		panic(err)
	}
	fakeHelper = filepath.Join(dir, "vani-fm")
	build := exec.Command("go", "build", "-o", fakeHelper, "./testdata/fakefm")
	if out, err := build.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		panic("build fake helper: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func backend(t *testing.T, mod func(*Config)) *Backend {
	t.Helper()
	cfg := Config{Path: fakeHelper, Timeout: 5 * time.Second, Descriptions: fieldtriage.Descriptions()}
	if mod != nil {
		mod(&cfg)
	}
	return New(cfg)
}

func question(label string) decide.Question {
	f := fieldtriage.Field{ID: "field-9f3a", Label: label, Kind: fieldtriage.KindText}
	if err := f.Validate(); err != nil {
		panic(err)
	}
	return fieldtriage.Question(f)
}

func one(t *testing.T, b *Backend, label string) decide.SlotReading {
	t.Helper()
	ans, err := b.Decide(context.Background(), question(label))
	if err != nil {
		t.Fatalf("Decide(%q): %v", label, err)
	}
	if ans.Backend != Name {
		t.Fatalf("backend %q", ans.Backend)
	}
	rd, ok := ans.Slots[fieldtriage.Slot]
	if !ok || len(ans.Slots) != 1 {
		t.Fatalf("slots %v", ans.Slots)
	}
	return rd
}

func TestNameAndServes(t *testing.T) {
	b := New(Config{})
	if b.Name() != Name || Name != "apple_fm" {
		t.Fatal(b.Name())
	}
	if !b.Serves(fieldtriage.Task) {
		t.Fatal("must serve career.form_field")
	}
	for _, task := range []string{"career.jd_triage", "career.kb_match", "", "career.form_field "} {
		if b.Serves(task) {
			t.Fatalf("serves %q", task)
		}
	}
	// A question of another task is not asked, whatever the router did.
	q := question("MODE:answered")
	q.Task = "career.jd_triage"
	ans, err := backend(t, nil).Decide(context.Background(), q)
	if err != nil || ans.Slots[fieldtriage.Slot].Reading != decide.NotAsked {
		t.Fatalf("%v %v", ans, err)
	}
}

func TestAnsweredIsOneOfTheOptions(t *testing.T) {
	rd := one(t, backend(t, nil), "MODE:answered")
	if rd.Reading != decide.Answered || rd.Value != fieldtriage.Fact {
		t.Fatalf("%+v", rd)
	}
}

func TestReadingsMapWithClosedDetails(t *testing.T) {
	cases := []struct {
		label  string
		want   decide.Reading
		detail string
	}{
		{"MODE:abstained", decide.Abstained, "declined"},
		{"MODE:refused", decide.Refused, "refusal"},
		{"MODE:unavailable", decide.Unavailable, "apple_intelligence_not_enabled"},
		{"MODE:bad_value", decide.Failed, "invalid_value"},
		{"MODE:freetext", decide.Refused, "unknown"},
		{"MODE:badversion", decide.Failed, "bad_version"},
		{"MODE:unknownreading", decide.Failed, "unknown_reading"},
		{"MODE:garbage", decide.Failed, "bad_output"},
		{"MODE:trailing", decide.Failed, "bad_output"},
		{"MODE:empty", decide.Failed, "bad_output"},
		{"MODE:exit1", decide.Failed, "helper_exit"},
		{"MODE:exit1json", decide.Failed, "helper_exit"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			// A fresh backend each: an Unavailable reading takes a backend down for the run.
			rd := one(t, backend(t, nil), c.label)
			if rd.Reading != c.want || rd.Detail != c.detail || rd.Value != "" || rd.Lappi != nil || rd.Jev != nil {
				t.Fatalf("%+v, want %s/%s", rd, c.want, c.detail)
			}
		})
	}
}

// The model being off or missing holds for the run: after the first Unavailable
// reading no helper is started again, so a page of questions does not spend the
// triage budget on starts and timeouts before the next backend is asked. A
// refusal is about one question and takes nothing down.
func TestUnavailableTakesTheBackendDownForTheRun(t *testing.T) {
	b := backend(t, nil)
	if rd := one(t, b, "MODE:refused"); rd.Reading != decide.Refused {
		t.Fatalf("%+v", rd)
	}
	if rd := one(t, b, "MODE:answered"); rd.Reading != decide.Answered {
		t.Fatalf("a refusal took the backend down: %+v", rd)
	}
	if rd := one(t, b, "MODE:unavailable"); rd.Reading != decide.Unavailable {
		t.Fatalf("%+v", rd)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	rd := one(t, b, "MODE:dumpreq:"+ran)
	if rd.Reading != decide.Unavailable || rd.Detail != "apple_intelligence_not_enabled" {
		t.Fatalf("after Unavailable: %+v", rd)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatalf("a helper was started after the backend went down: %v", err)
	}

	missing := New(Config{Path: filepath.Join(t.TempDir(), "absent"), Descriptions: fieldtriage.Descriptions()})
	for range 3 {
		if rd := one(t, missing, "Why do you want to work here?"); rd.Reading != decide.Unavailable || rd.Detail != "no_binary" {
			t.Fatalf("%+v", rd)
		}
	}
}

// A model that answers slowly but inside its watchdog must not use up the caller's
// triage bound one question at a time: once the helper wall time reaches the
// budget the backend stands down as "slow" and starts nothing more. The question
// that crossed the budget keeps its own reading.
func TestSlowModelTripsTheBackendDownAfterTheBudget(t *testing.T) {
	b := backend(t, func(c *Config) { c.Budget = 700 * time.Millisecond })
	if rd := one(t, b, "MODE:sleep:400"); rd.Reading != decide.Answered {
		t.Fatalf("under budget: %+v", rd)
	}
	if rd := one(t, b, "MODE:sleep:400"); rd.Reading != decide.Answered || rd.Detail != "" {
		t.Fatalf("the question that crossed the budget must keep its reading: %+v", rd)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	start := time.Now()
	rd := one(t, b, "MODE:dumpreq:"+ran)
	if rd.Reading != decide.Unavailable || rd.Detail != "slow" || rd.Value != "" {
		t.Fatalf("after the budget: %+v", rd)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("standing down took %v: it must answer at once", d)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatalf("a helper was started after the budget was spent: %v", err)
	}
	// It stays down for the run, for any question.
	if rd := one(t, b, "MODE:answered"); rd.Detail != "slow" {
		t.Fatalf("%+v", rd)
	}
}

func TestSlowBackendSendsTheRestToTheNextBackendAtOnce(t *testing.T) {
	// The slow helper answers with the first option; the stub with another, so the two are told apart.
	r := decide.NewRouter(backend(t, func(c *Config) { c.Budget = 300 * time.Millisecond }), stub{value: fieldtriage.Fact})
	first, err := r.Decide(context.Background(), question("MODE:sleep:400"))
	if err != nil {
		t.Fatal(err)
	}
	if rd := first.Slots[fieldtriage.Slot]; rd.Value != fieldtriage.Essay || !strings.Contains(rd.Detail, "backend="+Name) {
		t.Fatalf("the slow question still has its own answer: %+v", rd)
	}
	start := time.Now()
	for range 5 {
		ans, err := r.Decide(context.Background(), question("MODE:sleep:400"))
		if err != nil {
			t.Fatal(err)
		}
		if rd := ans.Slots[fieldtriage.Slot]; rd.Value != fieldtriage.Fact || !strings.Contains(rd.Detail, "backend=stub") {
			t.Fatalf("%+v", rd)
		}
	}
	// Five slow questions would be 2 s of helper time; standing down costs nothing.
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("five questions took %v after the budget was spent", d)
	}
}

func TestBudgetDoesNotTouchAFastModelOrAnEarlierDownReason(t *testing.T) {
	fast := backend(t, nil)
	for i := range 20 {
		if rd := one(t, fast, "MODE:answered"); rd.Reading != decide.Answered {
			t.Fatalf("question %d: %+v", i, rd)
		}
	}
	// A reading that already took the backend down keeps its own detail.
	b := backend(t, func(c *Config) { c.Budget = 100 * time.Millisecond; c.Timeout = 300 * time.Millisecond })
	if rd := one(t, b, "MODE:hang"); rd.Detail != "timeout" {
		t.Fatalf("%+v", rd)
	}
	if rd := one(t, b, "MODE:answered"); rd.Reading != decide.Unavailable || rd.Detail != "timeout" {
		t.Fatalf("the first reason wins: %+v", rd)
	}
}

// Questions queued behind the helper slots look at the budget when they get a
// slot, not only when they arrive: a page of questions must not each start a
// slow helper because all of them passed the check before any finished.
func TestBudgetIsRecheckedAfterWaitingForASlot(t *testing.T) {
	b := backend(t, func(c *Config) { c.Budget = 400 * time.Millisecond })
	type result struct {
		rd  decide.SlotReading
		err error
	}
	results := make(chan result, 8)
	for range cap(results) {
		go func() {
			ans, err := b.Decide(context.Background(), question("MODE:sleep:300"))
			results <- result{ans.Slots[fieldtriage.Slot], err}
		}()
	}
	answered, slow := 0, 0
	for range cap(results) {
		r := <-results
		if r.err != nil {
			t.Fatal(r.err)
		}
		switch {
		case r.rd.Reading == decide.Answered:
			answered++
		case r.rd.Reading == decide.Unavailable && r.rd.Detail == "slow":
			slow++
		default:
			t.Fatalf("%+v", r.rd)
		}
	}
	// MaxConcurrent run at once; one more pair can start before the first reaches the budget.
	if answered < MaxConcurrent || answered > 2*MaxConcurrent || answered+slow != cap(results) {
		t.Fatalf("answered %d, slow %d of %d", answered, slow, cap(results))
	}
}

func TestBudgetIsBounded(t *testing.T) {
	for _, c := range []struct{ in, want time.Duration }{
		{0, DefaultBudget}, {-time.Second, DefaultBudget}, {time.Second, time.Second},
		{MaxBudget, MaxBudget}, {time.Hour, MaxBudget},
	} {
		if got := New(Config{Budget: c.in}).cfg.Budget; got != c.want {
			t.Errorf("Budget %v -> %v, want %v", c.in, got, c.want)
		}
	}
	if DefaultBudget >= 45*time.Second || MaxBudget >= 45*time.Second {
		t.Fatal("the budget must leave room inside suggest's 45 s triage bound")
	}
}

// Questions queued for a helper slot behind the one that found the model off
// start no helper once they get the slot.
func TestQueuedQuestionsStartNoHelperAfterTheModelIsFoundOff(t *testing.T) {
	b := backend(t, nil)
	dir := t.TempDir()
	var wg sync.WaitGroup
	for range MaxConcurrent {
		wg.Add(1)
		go func() { defer wg.Done(); one(t, b, "MODE:slowoff:300") }()
	}
	time.Sleep(100 * time.Millisecond) // both slots are taken; the next questions wait for one
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rd := one(t, b, "MODE:dumpreq:"+filepath.Join(dir, strconv.Itoa(i))); rd.Reading != decide.Unavailable {
				t.Errorf("queued question %d: %+v", i, rd)
			}
		}()
	}
	wg.Wait()
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("%d helpers started after the model was found off", len(left))
	}
}

func TestOversizedOutputIsCutAndTheHelperStopped(t *testing.T) {
	start := time.Now()
	rd := one(t, backend(t, func(c *Config) { c.Timeout = 10 * time.Second }), "MODE:oversized")
	if rd.Reading != decide.Failed || rd.Detail != "output_too_large" {
		t.Fatalf("%+v", rd)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v: the helper was not stopped at the cap", d)
	}
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the helper never reported its pid")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHungHelperIsKilledAtTheTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	b := backend(t, func(c *Config) { c.Timeout = 400 * time.Millisecond })
	start := time.Now()
	rd := one(t, b, "MODE:hang:"+pidFile)
	if rd.Reading != decide.Unavailable || rd.Detail != "timeout" {
		t.Fatalf("%+v", rd)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("took %v", d)
	}
	if pid := readPID(t, pidFile); pidAlive(pid) {
		t.Fatalf("helper %d is still running", pid)
	}
}

func TestContextCancelKillsTheHelperAndReturnsTheContextError(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, err := backend(t, func(c *Config) { c.Timeout = 10 * time.Second }).Decide(ctx, question("MODE:hang:"+pidFile))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("took %v", d)
	}
	if pid := readPID(t, pidFile); pidAlive(pid) {
		t.Fatalf("helper %d is still running", pid)
	}
}

func TestAlreadyCanceledContextStartsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	_, err := backend(t, nil).Decide(ctx, question("MODE:hang:"+pidFile))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("the helper ran under a canceled context")
	}
}

func TestMissingBinaryIsUnavailableNotAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "vani-fm")
	for name, mod := range map[string]func(*Config){
		"path":     func(c *Config) { c.Path = missing },
		"relative": func(c *Config) { c.Path = "vani-fm" },
		"env": func(c *Config) {
			c.Path = ""
			c.Getenv = func(k string) string { return map[string]string{EnvBinary: missing}[k] }
		},
		"directory": func(c *Config) { c.Path = t.TempDir() },
		"not executable": func(c *Config) {
			p := filepath.Join(t.TempDir(), "vani-fm")
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			c.Path = p
		},
		"defaults": func(c *Config) {
			c.Path = ""
			c.Getenv = func(string) string { return "" }
			c.Executable = func() (string, error) { return filepath.Join(t.TempDir(), "Contents", "Helpers", "vani"), nil }
			c.AppPath = missing
		},
	} {
		t.Run(name, func(t *testing.T) {
			rd := one(t, backend(t, mod), "MODE:answered")
			if rd.Reading != decide.Unavailable || rd.Detail != "no_binary" {
				t.Fatalf("%+v", rd)
			}
		})
	}
}

func TestExplicitPathIsNotReplacedByAnotherBinary(t *testing.T) {
	// VANI_FM_BINARY names a missing helper; the working one at AppPath must not run instead.
	b := backend(t, func(c *Config) {
		c.Path = ""
		c.AppPath = fakeHelper
		c.Getenv = func(string) string { return filepath.Join(t.TempDir(), "gone") }
	})
	if b.Binary() != "" {
		t.Fatalf("ran %s", b.Binary())
	}
}

func TestBinaryLookupOrder(t *testing.T) {
	root := t.TempDir()
	helpers := filepath.Join(root, "Vani Apply.app", "Contents", "Helpers")
	macos := filepath.Join(root, "Vani Apply.app", "Contents", "MacOS")
	for _, d := range []string{helpers, macos} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sibling := filepath.Join(macos, "vani-fm")
	copyFile(t, fakeHelper, sibling)
	app := filepath.Join(root, "app-vani-fm")
	copyFile(t, fakeHelper, app)
	envBin := filepath.Join(root, "env-vani-fm")
	copyFile(t, fakeHelper, envBin)
	cfg := Config{
		Executable: func() (string, error) { return filepath.Join(helpers, "vani"), nil },
		AppPath:    app,
		Getenv:     func(string) string { return "" },
	}
	want := func(b *Backend, p string) {
		t.Helper()
		got, _ := filepath.EvalSymlinks(b.Binary())
		w, _ := filepath.EvalSymlinks(p)
		if got != w {
			t.Fatalf("binary %q, want %q", got, w)
		}
	}
	want(New(cfg), sibling)
	if err := os.Remove(sibling); err != nil {
		t.Fatal(err)
	}
	want(New(cfg), app)
	cfg.Getenv = func(k string) string { return map[string]string{EnvBinary: envBin}[k] }
	want(New(cfg), envBin)
	cfg.Path = fakeHelper
	want(New(cfg), fakeHelper)
}

// TestAssembledBundleLayout checks the rule against a real bundle that
// macos/scripts/bundle.sh assembled (VANI_BUNDLE_OUT=<dir> and
// VANI_TEST_BUNDLE=<dir>/"Vani Apply.app"): vani in Contents/Helpers must find
// vani-fm in Contents/MacOS.
func TestAssembledBundleLayout(t *testing.T) {
	app := os.Getenv("VANI_TEST_BUNDLE")
	if app == "" {
		t.Skip("VANI_TEST_BUNDLE is not set")
	}
	cfg := Config{
		Executable: func() (string, error) { return filepath.Join(app, "Contents", "Helpers", "vani"), nil },
		AppPath:    filepath.Join(t.TempDir(), "absent"),
		Getenv:     func(string) string { return "" },
	}
	got, want := New(cfg).Binary(), filepath.Join(app, "Contents", "MacOS", "vani-fm")
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(want)
	if got == "" || gotReal != wantReal {
		t.Fatalf("Binary() = %q, want %q", got, want)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	raw, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, raw, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestStartFailureIsUnavailable(t *testing.T) {
	// Executable bit set, but not a runnable image.
	p := filepath.Join(t.TempDir(), "vani-fm")
	if err := os.WriteFile(p, []byte("not a binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	rd := one(t, backend(t, func(c *Config) { c.Path = p }), "MODE:answered")
	if rd.Reading != decide.Unavailable || rd.Detail != "start_failed" {
		t.Fatalf("%+v", rd)
	}
}

func TestRequestCarriesTheLabelAndTypeOnly(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "req.json")
	q := question("MODE:dumpreq:" + dump)
	if _, err := backend(t, nil).Decide(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"v":1`, `"task":"career.form_field"`, "Field type: text", "Question label (untrusted", `"essay"`, "Voluntary demographic self-identification"} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %q: %s", want, s)
		}
	}
	// The record id is the extension's bookkeeping, not model input.
	if strings.Contains(s, "field-9f3a") || strings.Contains(s, "record_id") {
		t.Errorf("request carries the record id: %s", s)
	}
}

func TestHelperGetsOnlyHOME(t *testing.T) {
	t.Setenv("HOME", "/Users/someone")
	t.Setenv("OPENROUTER_API_KEY", "sk-canary-value")
	t.Setenv("ANTHROPIC_API_KEY", "sk-canary-value")
	dump := filepath.Join(t.TempDir(), "env")
	if _, err := backend(t, nil).Decide(context.Background(), question("MODE:dumpenv:"+dump)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != "HOME=/Users/someone" {
		t.Fatalf("helper environment %q", got)
	}
}

func TestQuestionsOutsideTheHelperBoundsAreRefusedBeforeAnyProcess(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	b := backend(t, nil)
	long := question("MODE:hang:" + pidFile)
	long.Context = []byte(strings.Repeat("x", maxQuestionBytes))
	ans, err := b.Decide(context.Background(), long)
	if err != nil || ans.Slots[fieldtriage.Slot].Reading != decide.Refused || ans.Slots[fieldtriage.Slot].Detail != "question_too_large" {
		t.Fatalf("%+v %v", ans, err)
	}
	for name, opts := range map[string][]string{
		"none option":   {"essay", "none"},
		"long option":   {"essay", strings.Repeat("o", maxOptionBytes+1)},
		"newline":       {"essay", "a\nb"},
		"padded option": {"essay", " other"},
	} {
		q := question("MODE:hang:" + pidFile)
		q.Slots = []decide.Slot{{Name: fieldtriage.Slot, Options: opts}}
		ans, err := b.Decide(context.Background(), q)
		if err != nil || ans.Slots[fieldtriage.Slot].Reading != decide.Refused || ans.Slots[fieldtriage.Slot].Detail != "bad_options" {
			t.Fatalf("%s: %+v %v", name, ans, err)
		}
	}
	big := backend(t, func(c *Config) {
		c.Descriptions = map[string]map[string]string{fieldtriage.Slot: {"essay": strings.Repeat("d", maxInstructionsBytes)}}
	})
	ans, err = big.Decide(context.Background(), question("MODE:hang:"+pidFile))
	if err != nil || ans.Slots[fieldtriage.Slot].Detail != "instructions_too_large" {
		t.Fatalf("%+v %v", ans, err)
	}
	if _, err := os.Stat(pidFile); err == nil {
		t.Fatal("a helper ran for a question outside its bounds")
	}
}

func TestConcurrentQuestionsRunAtMostMaxConcurrentHelpers(t *testing.T) {
	// Slow answers, not hangs: a timeout takes the backend down for the run, so later
	// questions would return at once and say nothing about how many ran together.
	b := backend(t, nil)
	done := make(chan decide.SlotReading, 6)
	start := time.Now()
	for i := 0; i < 6; i++ {
		go func() {
			ans, _ := b.Decide(context.Background(), question("MODE:sleep:300"))
			done <- ans.Slots[fieldtriage.Slot]
		}()
	}
	for i := 0; i < 6; i++ {
		if rd := <-done; rd.Reading != decide.Answered {
			t.Fatalf("%+v", rd)
		}
	}
	// 6 answers at 2 at a time, 300 ms each, take three rounds.
	if d := time.Since(start); d < 850*time.Millisecond {
		t.Fatalf("6 helpers finished in %v: more than %d ran at once", d, MaxConcurrent)
	}
}

func TestRouterMovesOnWhenTheHelperDoesNotAnswer(t *testing.T) {
	second := stub{value: fieldtriage.Essay}
	for _, label := range []string{"MODE:refused", "MODE:unavailable", "MODE:garbage"} {
		r := decide.NewRouter(backend(t, nil), second)
		ans, err := r.Decide(context.Background(), question(label))
		if err != nil {
			t.Fatal(err)
		}
		rd := ans.Slots[fieldtriage.Slot]
		if rd.Reading != decide.Answered || rd.Value != fieldtriage.Essay || !strings.Contains(rd.Detail, "backend=stub") {
			t.Fatalf("%s: %+v", label, rd)
		}
	}
	r := decide.NewRouter(backend(t, nil), second)
	ans, _ := r.Decide(context.Background(), question("MODE:answered"))
	if rd := ans.Slots[fieldtriage.Slot]; rd.Value != fieldtriage.Fact || !strings.Contains(rd.Detail, "backend="+Name) {
		t.Fatalf("%+v", rd)
	}
}

type stub struct{ value string }

func (stub) Name() string       { return "stub" }
func (stub) Serves(string) bool { return true }
func (s stub) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	return decide.Answer{Backend: "stub", Slots: map[string]decide.SlotReading{fieldtriage.Slot: {Reading: decide.Answered, Value: s.value}}}, nil
}

// The helper's reply codes are the Swift file's; the two lists must not drift.
func TestKnownDetailsMatchTheSwiftHelper(t *testing.T) {
	raw, err := os.ReadFile("../../../macos/Sources/VaniApplyKit/OnDeviceDecider.swift")
	if err != nil {
		t.Skipf("Swift source not found: %v", err)
	}
	found := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`detail: "([a-z_]+)"`),
		regexp.MustCompile(`code = "([a-z_]+)"`),
		regexp.MustCompile(`badRequest\("([a-z_]+)"\)`),
	} {
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			found[m[1]] = true
		}
	}
	if len(found) < 20 {
		t.Fatalf("found only %d codes in the Swift source: the extraction broke", len(found))
	}
	for c := range found {
		if !knownDetails[c] {
			t.Errorf("Swift reports %q, which fmbackend does not know", c)
		}
	}
	for c := range knownDetails {
		if !found[c] {
			t.Errorf("fmbackend knows %q, which the Swift helper never reports", c)
		}
	}
}

// TestRealHelper runs the built vani-fm when VANI_FM_BINARY names it (macos
// `swift build`); the model may be off, so any non-failed reading passes, but a
// value must be an option. It sends only two harmless labels.
func TestRealHelper(t *testing.T) {
	if os.Getenv(EnvBinary) == "" {
		t.Skip("VANI_FM_BINARY is not set")
	}
	b := New(Config{Descriptions: fieldtriage.Descriptions()})
	for _, label := range []string{"Why do you want to work here?", "What is your LinkedIn URL?"} {
		start := time.Now()
		ans, err := b.Decide(context.Background(), question(label))
		if err != nil {
			t.Fatal(err)
		}
		rd := ans.Slots[fieldtriage.Slot]
		t.Logf("%q -> %+v in %v", label, rd, time.Since(start))
		if rd.Reading == decide.Failed {
			t.Fatalf("%q: %+v", label, rd)
		}
		if rd.Reading == decide.Answered && !slices.Contains(question(label).Slots[0].Options, rd.Value) {
			t.Fatalf("%q: value %q is not an option", label, rd.Value)
		}
	}
}

func TestMaxTimeoutIsClamped(t *testing.T) {
	if got := New(Config{Timeout: time.Hour}).cfg.Timeout; got != MaxTimeout {
		t.Fatal(got)
	}
	if got := New(Config{}).cfg.Timeout; got != DefaultTimeout {
		t.Fatal(got)
	}
}
