package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// setup points the CLI at a temp config and state dir.
func setup(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.json")
	stateDir = filepath.Join(dir, "state")
	t.Setenv("VANI_CONFIG", cfgPath)
	t.Setenv("VANI_STATE_DIR", stateDir)
	return cfgPath, stateDir
}

func do(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

func mustCode(t *testing.T, want int, args ...string) (stdout, stderr string) {
	t.Helper()
	code, o, e := do(t, args...)
	if code != want {
		t.Fatalf("vani %v: exit %d, want %d\nstdout: %s\nstderr: %s", args, code, want, o, e)
	}
	return o, e
}

func TestUsageErrorsExit2(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init") // so a usage error is not masked by a missing config
	for _, args := range [][]string{
		nil,
		{"bogus"},
		{"init", "--nope"},
		{"init", "extra"},
		{"config"},
		{"config", "wat"},
		{"watchlist"},
		{"watchlist", "wat"},
		{"watchlist", "add"},
		{"watchlist", "add", "--ats", "gh", "--token", "x"},
		{"watchlist", "add", "--ats", "zz", "--token", "x", "--company", "X"},
		{"watchlist", "remove", "--ats", "gh"},
		{"digest", "--date", "yesterday"},
		{"launchd"},
		{"launchd", "plist", "--binary", "relative/vani"},
	} {
		code, _, stderr := do(t, args...)
		if code != 2 {
			t.Errorf("vani %v: exit %d, want 2 (stderr %q)", args, code, stderr)
		}
	}
}

func TestHelpExitsZero(t *testing.T) {
	setup(t)
	o, _ := mustCode(t, 0, "help")
	if !strings.Contains(o, "usage: vani") {
		t.Errorf("help text: %q", o)
	}
	mustCode(t, 0, "init", "-h")
}

func TestInitRefusesOverwrite(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	st, err := os.Stat(cfgPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config not created 0600: %v %v", err, st)
	}
	if err := os.WriteFile(cfgPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, e := mustCode(t, 1, "init")
	if !strings.Contains(e, "refusing to overwrite") {
		t.Errorf("stderr %q", e)
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != "keep" {
		t.Error("existing config overwritten")
	}
}

func TestConfigCheck(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 1, "config", "check") // missing file
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "config", "check")
	if !strings.HasPrefix(o, "ok:") {
		t.Errorf("stdout %q", o)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, e := mustCode(t, 1, "config", "check")
	if !strings.Contains(e, "version is 2") {
		t.Errorf("stderr %q", e)
	}
}

func TestWatchlistLifecycle(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")

	// Host not allowed yet: refused, config untouched.
	before, _ := os.ReadFile(cfgPath)
	_, e := mustCode(t, 1, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme")
	if !strings.Contains(e, "boards-api.greenhouse.io") || !strings.Contains(e, "--allow-host") {
		t.Errorf("missing-host error not helpful: %q", e)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
		t.Error("config changed by refused add")
	}

	mustCode(t, 0, "watchlist", "add", "--ats", "gh", "--token", "acme", "--company", "Acme", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "lv", "--token", "globex", "--company", "Globex", "--region", "eu", "--allow-host")
	mustCode(t, 1, "watchlist", "add", "--ats", "gh", "--token", "ACME", "--company", "Acme again", "--allow-host") // duplicate

	o, _ := mustCode(t, 0, "watchlist", "list")
	if !strings.Contains(o, "gh\tacme\t-\tAcme") || !strings.Contains(o, "lv\tglobex\teu\tGlobex") {
		t.Errorf("list output %q", o)
	}
	mustCode(t, 0, "config", "check")

	mustCode(t, 0, "watchlist", "remove", "--ats", "gh", "--token", "acme")
	mustCode(t, 1, "watchlist", "remove", "--ats", "gh", "--token", "acme")
	o, _ = mustCode(t, 0, "watchlist", "list")
	if strings.Contains(o, "acme") || !strings.Contains(o, "globex") {
		t.Errorf("after remove: %q", o)
	}
}

func TestLaunchdPlistPrintsAndInstallsNothing(t *testing.T) {
	_, stateDir := setup(t)
	o, _ := mustCode(t, 0, "launchd", "plist", "--binary", "/opt/v&v/vani", "--label", "dev.test")
	for _, want := range []string{"<string>dev.test</string>", "/opt/v&amp;v/vani", "<string>daemon</string>", filepath.Join(stateDir, "logs", "vani.err.log")} {
		if !strings.Contains(o, want) {
			t.Errorf("plist missing %q:\n%s", want, o)
		}
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Error("launchd plist touched the state dir")
	}
}

// TestRealWiring drives the real buildApp (wire.go) on a temp state dir with
// no credentials in the environment: status works against the real ledger,
// and run-once fails loudly because no decision backend can be built. No
// network is touched: the default watchlist is empty.
func TestRealWiring(t *testing.T) {
	setup(t)
	for _, k := range []string{"OPENROUTER_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(k, "")
	}
	mustCode(t, 0, "init")
	o, _ := mustCode(t, 0, "status")
	if !strings.Contains(o, "postings: 0") {
		t.Fatalf("status output %q", o)
	}
	_, e := mustCode(t, 1, "run-once")
	if !strings.Contains(e, "no decision backend") {
		t.Fatalf("run-once stderr %q", e)
	}
}

// memLedger is an in-memory ledger.Ledger.
type memLedger struct {
	mu   sync.Mutex
	recs map[posting.Key]*ledger.Record
	fail bool
}

func (m *memLedger) Append(_ context.Context, e ledger.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	r, ok := m.recs[e.Key]
	if !ok {
		return errors.New("unknown key")
	}
	r.Status, r.LastEvent = e.Status, e.At
	return nil
}
func (m *memLedger) Get(k posting.Key) (ledger.Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[k]
	if !ok {
		return ledger.Record{}, false
	}
	return *r, true
}
func (m *memLedger) All() []ledger.Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ledger.Record
	for _, r := range m.recs {
		out = append(out, *r)
	}
	return out
}
func (m *memLedger) Close() error { return nil }

type fakeNotifier struct{ calls []string }

func (f *fakeNotifier) Notify(_ context.Context, title, body string) error {
	f.calls = append(f.calls, title+"|"+body)
	return nil
}

func wire(t *testing.T, l *memLedger, n *fakeNotifier, runErr error) *int {
	t.Helper()
	old, oldNow := buildApp, now
	t.Cleanup(func() { buildApp, now = old, oldNow })
	now = func() time.Time { return time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC) }
	runs := new(int)
	buildApp = func(config.Config, string, io.Writer) (*App, error) {
		return &App{Ledger: l, Notifier: n, RunOnce: func(context.Context) error { *runs++; return runErr }}, nil
	}
	return runs
}

func fixture() *memLedger {
	at := time.Date(2026, 10, 18, 15, 0, 0, 0, time.UTC)
	mk := func(k string, st posting.Status) *ledger.Record {
		return &ledger.Record{
			Key: posting.Key(k), Status: st, FirstSeen: at, LastEvent: at,
			Posting:     posting.Posting{Company: "Acme", Title: "Eng " + k, URL: "https://x.example/" + k},
			Annotations: map[string]string{"score.total": "70"},
		}
	}
	return &memLedger{recs: map[posting.Key]*ledger.Record{
		"gh:a:1": mk("gh:a:1", posting.Explained),
		"gh:a:2": mk("gh:a:2", posting.Unsure),
		"gh:a:3": mk("gh:a:3", posting.Dead),
		"gh:a:4": mk("gh:a:4", posting.Dropped),
	}}
}

func TestRunOncePublishesDigestAndMarksDigested(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l, n := fixture(), &fakeNotifier{}
	runs := wire(t, l, n, nil)

	o, _ := mustCode(t, 0, "run-once")
	if *runs != 1 || !strings.Contains(o, "(2 roles)") {
		t.Errorf("runs=%d stdout=%q", *runs, o)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "digests", "2026-10-18.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "New today: 4 | Dropped today: 1 | Dead today: 1") {
		t.Errorf("footer:\n%s", data)
	}
	for _, k := range []posting.Key{"gh:a:1", "gh:a:2"} {
		if r, _ := l.Get(k); r.Status != posting.Digested {
			t.Errorf("%s status %s, want digested", k, r.Status)
		}
	}
	if r, _ := l.Get("gh:a:3"); r.Status != posting.Dead {
		t.Error("dead record was touched")
	}
	if len(n.calls) != 1 || !strings.Contains(n.calls[0], "2 roles") {
		t.Errorf("notifications: %v", n.calls)
	}

	// Second run the same day: nothing new, nothing rewritten, no notification.
	mustCode(t, 0, "run-once")
	if len(n.calls) != 1 {
		t.Errorf("empty digest notified: %v", n.calls)
	}
}

func TestRunOnceFailureSkipsDigest(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, errors.New("all boards down"))
	_, e := mustCode(t, 1, "run-once")
	if !strings.Contains(e, "all boards down") {
		t.Errorf("stderr %q", e)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "digests")); !os.IsNotExist(err) {
		t.Error("digest written after a failed run")
	}
}

func TestMarkDigestedFailureIsReported(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	l.fail = true
	wire(t, l, &fakeNotifier{}, nil)
	_, e := mustCode(t, 1, "run-once")
	if !strings.Contains(e, "disk full") {
		t.Errorf("stderr %q", e)
	}
}

func TestDigestDateAndStatus(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l := fixture()
	wire(t, l, &fakeNotifier{}, nil)

	o, _ := mustCode(t, 0, "status")
	for _, want := range []string{"postings: 4", "explained  1", "unsure     1", "boards: 0"} {
		if !strings.Contains(o, want) {
			t.Errorf("status missing %q:\n%s", want, o)
		}
	}
	mustCode(t, 0, "digest", "--date", "2026-10-01")
	if _, err := os.Stat(filepath.Join(stateDir, "digests", "2026-10-01.md")); err != nil {
		t.Errorf("dated digest missing: %v", err)
	}
}

func TestDaemonExitsCleanlyOnCancel(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	wire(t, fixture(), &fakeNotifier{}, nil)
	old := signalContext
	t.Cleanup(func() { signalContext = old })
	signalContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	_, e := mustCode(t, 0, "daemon")
	if !strings.Contains(e, "daemon started") {
		t.Errorf("stderr %q", e)
	}
}

func TestCountsForUsesScheduleZone(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip(err)
	}
	// 2026-10-19 03:00 UTC is still Oct 18 in Los Angeles.
	seen := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC)
	other := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	recs := []ledger.Record{
		{Status: posting.Live, FirstSeen: seen},
		{Status: posting.Live, FirstSeen: other},
		{Status: posting.Dead, FirstSeen: other, LastEvent: seen},
		{Status: posting.Dropped, FirstSeen: other, LastEvent: other},
	}
	c := countsFor(recs, time.Date(2026, 10, 18, 9, 0, 0, 0, loc), loc)
	if c.New != 1 || c.Dead != 1 || c.Dropped != 0 {
		t.Errorf("counts %+v", c)
	}
}

func TestWatchlistAddWorkdayAndPage(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	mustCode(t, 0, "watchlist", "add", "--ats", "wd", "--token", "acme/External", "--company", "Acme",
		"--host", "acme.wd5.myworkdayjobs.com", "--search", "machine learning", "--allow-host")
	mustCode(t, 0, "watchlist", "add", "--ats", "pg", "--token", "initech", "--company", "Initech",
		"--url", "https://careers.initech.example/jobs", "--link-pattern", `/jobs/\d+$`, "--allow-host")
	_, e := mustCode(t, 2, "watchlist", "add", "--ats", "wd", "--token", "acme/Ext2", "--company", "Acme", "--host", "evil.example.com")
	if !strings.Contains(e, "needs host") {
		t.Fatalf("stderr %q", e)
	}
	mustCode(t, 0, "config", "check")
	o, _ := mustCode(t, 0, "watchlist", "list")
	if !strings.Contains(o, "acme/External") || !strings.Contains(o, "initech") {
		t.Fatalf("list %q", o)
	}
}
