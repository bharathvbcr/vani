package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// seedShadowed writes count triaged postings "gh:<prefix>:NNN", each with a
// shadow reading on work_mode, and labels the first labelled of them. Labels
// are stamped a second apart from base so label-time order is key order.
func seedShadowed(t *testing.T, stateDir, prefix string, count, labelled int, base time.Time) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := eval.NewStore(filepath.Join(stateDir, "eval", "labels"), filepath.Join(stateDir, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	s.Now = func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }
	for i := 0; i < count; i++ {
		key := posting.Key(fmt.Sprintf("gh:%s:%03d", prefix, i))
		p := posting.Posting{Key: key, ATS: "gh", Company: "Acme", Title: "Engineer", Description: "Build things."}
		ann := map[string]string{
			"triage.work_mode": "remote", "triage.work_mode.reading": "answered",
			"triage.work_mode.backend": "d1-ultra", "triage.work_mode.p": "0.9000",
			"triage.work_mode.shadow": "remote", "triage.work_mode.shadow_reading": "answered",
			"triage.work_mode.shadow_backend": "local",
		}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: key, Status: posting.Triaged, Posting: &p, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
		if i < labelled {
			if err := s.Put(eval.Entry{Key: key, Labels: map[string]string{"work_mode": "remote"}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// answerAll feeds label sessions: option 1 for every slot of every posting.
func answerAll(t *testing.T) {
	t.Helper()
	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(strings.Repeat("1\n", 4000))
}

func windowMembers(t *testing.T, stateDir string) map[string]int {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(stateDir, "eval", "labels"), filepath.Join(stateDir, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.WindowMembers()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, w := range m {
		out[w]++
	}
	return out
}

func TestLabelWindowFillsAndCapsAtFreePlaces(t *testing.T) {
	_, stateDir := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	// 97 labelled (window-1 has 3 free places), 10 more the shadow was asked about.
	seedShadowed(t, stateDir, "a", 107, 97, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	answerAll(t)
	o, e := mustCode(t, 0, "label", "--window", "window-1", "-n", "10")
	if !strings.Contains(o, "labelled 3 of 3 queued") {
		t.Fatalf("-n 10 with 3 free places:\n%s\n%s", o, e)
	}
	if got := windowMembers(t, stateDir); got["window-1"] != 100 || got["window-2"] != 0 {
		t.Fatalf("members %v", got)
	}
}

func TestLabelWindowRefusals(t *testing.T) {
	_, stateDir := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	seedShadowed(t, stateDir, "a", 120, 40, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	answerAll(t)

	// Unknown window: usage error naming the registered ones.
	code, _, e := do(t, "label", "--window", "window-3")
	if code != 2 || !strings.Contains(e, "window-1") {
		t.Fatalf("unknown window: exit %d, %q", code, e)
	}
	// window-2 while window-1 is still filling: refused, and nothing is labelled.
	code, o, e := do(t, "label", "--window", "window-2")
	if code != 2 || !strings.Contains(e, "window-1") || strings.Contains(o, "labelled") {
		t.Fatalf("window-2 while window-1 fills: exit %d\n%s\n%s", code, o, e)
	}
	if got := windowMembers(t, stateDir); got["window-1"] != 40 || got["window-2"] != 0 {
		t.Fatalf("refusal changed the windows: %v", got)
	}
}

func TestLabelWindowOnClosedWindows(t *testing.T) {
	_, stateDir := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	seedShadowed(t, stateDir, "a", 230, 100, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	answerAll(t)

	// window-1 is closed (100 labelled): asking for it is refused and points at window-2.
	code, o, e := do(t, "label", "--window", "window-1")
	if code != 2 || !strings.Contains(e, "window-2") || strings.Contains(o, "labelled") {
		t.Fatalf("closed window-1: exit %d\n%s\n%s", code, o, e)
	}
	// window-2 takes 100 more; then every window is closed.
	o, e = mustCode(t, 0, "label", "--window", "window-2", "-n", "150")
	if !strings.Contains(o, "labelled 100 of 100 queued") {
		t.Fatalf("window-2 fill:\n%s\n%s", o, e)
	}
	if got := windowMembers(t, stateDir); got["window-1"] != 100 || got["window-2"] != 100 {
		t.Fatalf("members %v", got)
	}
	code, o, e = do(t, "label", "--window", "window-2")
	if code != 1 || !strings.Contains(e, "every window is closed") || strings.Contains(o, "labelled") {
		t.Fatalf("all closed: exit %d\n%s\n%s", code, o, e)
	}
}

// Postings the shadow was not asked about are never queued for a window.
func TestLabelWindowQueuesOnlyShadowAsked(t *testing.T) {
	_, stateDir := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	seedShadowed(t, stateDir, "a", 2, 0, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	p := posting.Posting{Key: "gh:plain:1", ATS: "gh", Company: "Acme", Title: "Engineer", Description: "x"}
	ann := map[string]string{"triage.work_mode": "remote", "triage.work_mode.reading": "answered", "triage.work_mode.backend": "d1-ultra"}
	if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Triaged, Posting: &p, Annotations: ann}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	answerAll(t)
	o, _ := mustCode(t, 0, "label", "--window", "window-1", "-n", "10")
	if !strings.Contains(o, "labelled 2 of 2 queued") || strings.Contains(o, "gh:plain:1") {
		t.Fatalf("an unasked posting was queued:\n%s", o)
	}
}

// labelWindowHelper is not a test: it is the body two real `vani label
// --window` processes run (the test binary re-executed), so two writers of
// windows.json really are two processes.
func TestLabelWindowHelper(t *testing.T) {
	n := os.Getenv("VANI_WL_HELPER_N")
	if n == "" {
		t.Skip("helper for TestLabelWindowTwoProcesses")
	}
	stdin = strings.NewReader(strings.Repeat("1\n", 4000))
	var o, e bytes.Buffer
	code := run([]string{"label", "--window", "window-1", "-n", n}, &o, &e)
	fmt.Print(o.String(), e.String())
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

// Two processes label for window-1 at once, each allowed all 100 places. The
// labels lock is held across sync+write, so windows.json must come out valid,
// within each window's size, with no posting in two windows.
func TestLabelWindowTwoProcesses(t *testing.T) {
	_, stateDir := setup(t)
	t.Setenv("VANI_NO_KEYCHAIN", "1")
	mustCode(t, 0, "init")
	seedShadowed(t, stateDir, "a", 160, 0, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	var outs [2]bytes.Buffer
	var cmds [2]*exec.Cmd
	for i := range cmds {
		c := exec.Command(os.Args[0], "-test.run=^TestLabelWindowHelper$", "-test.v")
		c.Env = append(os.Environ(), "VANI_WL_HELPER_N=100")
		c.Stdout, c.Stderr = &outs[i], &outs[i]
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = c
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outs[i].String())
		}
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, "eval", "labels", eval.WindowsFile))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		V       int
		Members map[string]struct{ Window string }
	}
	if err := json.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("windows.json corrupt after two writers: %v\n%s", err, raw)
	}
	counts := map[string]int{}
	for _, w := range wf.Members {
		counts[w.Window]++
	}
	if counts["window-1"] != 100 {
		t.Fatalf("window-1 holds %d: %v", counts["window-1"], counts)
	}
	if counts["window-1"]+counts["window-2"] != len(wf.Members) {
		t.Fatalf("members %v", counts)
	}
	labels, _, _ := mustLabels(t, stateDir)
	if len(wf.Members) != len(labels) {
		t.Fatalf("%d labelled postings but %d filed: a label was lost or left unfiled", len(labels), len(wf.Members))
	}
	// eval agrees with the file.
	o, _ := mustCode(t, 0, "eval", "--window")
	if !strings.Contains(o, "### window-1: 100/100 postings (closed)") {
		t.Fatalf("eval --window:\n%s", o)
	}
}

func mustLabels(t *testing.T, stateDir string) (map[posting.Key]eval.Entry, int, error) {
	t.Helper()
	s, err := eval.NewStore(filepath.Join(stateDir, "eval", "labels"), filepath.Join(stateDir, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	return s.Labels()
}

// Without --window, vani eval prints what it printed before; with it the
// output begins with exactly that report and ends with the windows section,
// also when --held-out is given (all-labels, then held-out, then windows).
func TestEvalWindowFlag(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 45)

	plain, _ := mustCode(t, 0, "eval")
	if strings.Contains(plain, "Label windows") {
		t.Fatalf("plain eval mentions windows:\n%s", plain)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "eval", "labels", eval.WindowsFile)); err == nil {
		t.Fatal("a plain eval wrote windows.json")
	}
	win, _ := mustCode(t, 0, "eval", "--window")
	if !strings.HasPrefix(win, plain) {
		t.Fatalf("--window output does not begin with the plain report:\n%s", win)
	}
	for _, frag := range []string{"## Label windows", "### window-1: 45/100 postings (open)", "### window-2: 0/100 postings (open)", "window-1 not closed (45/100 postings)", "window-2 not closed (0/100 postings)"} {
		if !strings.Contains(win, frag) {
			t.Errorf("--window output lacks %q:\n%s", frag, win)
		}
	}
	held, _ := mustCode(t, 0, "eval", "--held-out", "d1-vani-jdtriage-2026-10-18")
	both, _ := mustCode(t, 0, "eval", "--held-out", "d1-vani-jdtriage-2026-10-18", "--window")
	if !strings.HasPrefix(both, held) || !strings.HasSuffix(both, strings.TrimPrefix(win, plain)) {
		t.Fatalf("--held-out --window is not held-out output followed by the windows section:\n%s", both)
	}
}

// A torn label line or a bad windows.json: plain eval still reports; the
// window report refuses and prints no figures.
func TestEvalWindowRefusals(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	seedEval(t, stateDir, 5)
	labels := filepath.Join(stateDir, "eval", "labels")

	if err := os.WriteFile(filepath.Join(labels, eval.WindowsFile), []byte(`{"v":2,"windows":[{"name":"window-1","size":50}],"members":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mustCode(t, 0, "eval")
	o, e := mustCode(t, 1, "eval", "--window")
	if o != "" || !strings.Contains(e, "pre-registered") {
		t.Fatalf("changed registration: stdout %q stderr %q", o, e)
	}
	if err := os.WriteFile(filepath.Join(labels, eval.WindowsFile), []byte(`{"v":2,"win`), 0o600); err != nil {
		t.Fatal(err)
	}
	o, e = mustCode(t, 1, "eval", "--window")
	if o != "" || !strings.Contains(e, "unreadable") {
		t.Fatalf("torn windows.json: stdout %q stderr %q", o, e)
	}
	if err := os.Remove(filepath.Join(labels, eval.WindowsFile)); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(labels, eval.LabelsFile), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"v":1,"key":"gh:acme:999","lab`)
	f.Close()
	o, e = mustCode(t, 1, "eval", "--window")
	if o != "" || !strings.Contains(e, "unreadable") {
		t.Fatalf("torn label line: stdout %q stderr %q", o, e)
	}
}
