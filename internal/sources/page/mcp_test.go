package page

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	pageA = "https://careers.example.com/jobs/a"
	pageB = "https://careers.example.com/jobs/b"
	pageC = "https://other.example.org/jobs/c"
)

// fakeLP returns an MCPBrowser whose lightpanda is this test binary acting
// as fakeMCP with pages, and the path of the fake's event log.
func fakeLP(t *testing.T, pages map[string]fakePage, timeout time.Duration, extra ...string) (*MCPBrowser, string) {
	t.Helper()
	dir := t.TempDir()
	pagesPath, logPath := filepath.Join(dir, "pages.json"), filepath.Join(dir, "log.jsonl")
	b, err := json.Marshal(pages)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pagesPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	flags := append([]string{"--fake-pages=" + pagesPath, "--fake-log=" + logPath}, extra...)
	br, err := NewMCPBrowser(bin, flags, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = br.Close() })
	return br, logPath
}

func events(t *testing.T, logPath string) []fakeEvent {
	t.Helper()
	f, err := os.Open(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []fakeEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e fakeEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("bad log line %q: %v", sc.Text(), err)
		}
		out = append(out, e)
	}
	return out
}

func starts(t *testing.T, logPath string) int {
	t.Helper()
	n := 0
	for _, e := range events(t, logPath) {
		if e.Ev == "start" {
			n++
		}
	}
	return n
}

// toolCalls lists the fake's tool calls as "tool url" (url empty for html).
func toolCalls(t *testing.T, logPath string) []string {
	t.Helper()
	var out []string
	for _, e := range events(t, logPath) {
		if e.Ev != "req" || e.Method != "tools/call" {
			continue
		}
		var a struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(e.Arguments, &a)
		out = append(out, strings.TrimSpace(e.Tool+" "+a.URL))
	}
	return out
}

func mustLoad(t *testing.T, b Browser, u string) string {
	t.Helper()
	doc, err := b.Load(context.Background(), u)
	if err != nil {
		t.Fatalf("Load %s: %v", u, err)
	}
	return string(doc)
}

// One process serves every page: started once, handshaken once, each page
// read by goto then html, with exactly the arguments the contract names.
func TestMCPLoadsThroughOneProcess(t *testing.T) {
	t.Setenv("VANI_SECRET", "hunter2")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	b, log := fakeLP(t, map[string]fakePage{
		pageA: {HTML: "<html>a</html>"}, pageB: {HTML: "<html>b</html>"}, pageC: {HTML: "<html>c</html>"},
	}, 5*time.Second, "--obey-robots", "--user-agent", "Vani/0.1 (+personal job-listing reader)")
	for u, want := range map[string]string{pageA: "<html>a</html>", pageB: "<html>b</html>", pageC: "<html>c</html>"} {
		if got := mustLoad(t, b, u); got != want {
			t.Errorf("%s: %q, want %q", u, got, want)
		}
	}
	evs := events(t, log)
	if n := starts(t, log); n != 1 {
		t.Fatalf("%d processes for three pages, want 1", n)
	}
	start := evs[0]
	if start.Args[0] != "mcp" || !slices.Contains(start.Args, "--obey-robots") ||
		!slices.Contains(start.Args, "Vani/0.1 (+personal job-listing reader)") {
		t.Errorf("argv %q", start.Args)
	}
	env := strings.Join(start.Env, "\n")
	for _, leaked := range []string{"VANI_SECRET", "hunter2", "ANTHROPIC_API_KEY", "sk-test"} {
		if strings.Contains(env, leaked) {
			t.Errorf("child environment leaks %q", leaked)
		}
	}
	if !strings.Contains(env, "LIGHTPANDA_DISABLE_TELEMETRY=1") || !strings.Contains(env, "PATH=") {
		t.Errorf("child environment lacks telemetry-off or PATH:\n%s", env)
	}
	if wd, _ := os.Getwd(); start.Cwd == wd || !strings.Contains(filepath.Base(start.Cwd), "vani-lightpanda-") {
		t.Errorf("child cwd %q: want a fresh directory of its own", start.Cwd)
	}
	if evs[1].Method != "initialize" || evs[2].Method != "notifications/initialized" {
		t.Errorf("handshake %q %q", evs[1].Method, evs[2].Method)
	}
	for _, e := range evs {
		if e.Ev != "req" || e.Method != "tools/call" {
			continue
		}
		var a map[string]any
		_ = json.Unmarshal(e.Arguments, &a)
		switch e.Tool {
		case "goto":
			if len(a) != 2 || a["timeout"] != float64(gotoTimeoutMs) || a["url"] == nil {
				t.Errorf("goto arguments %v", a)
			}
		case "html":
			if len(a) != 0 { // a url would navigate again; maxBytes would truncate
				t.Errorf("html arguments %v, want none", a)
			}
		default:
			t.Errorf("unexpected tool %q", e.Tool)
		}
	}

	// Close ends the process gracefully (stdin closed, it saw EOF) and
	// removes its directory.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	evs = events(t, log)
	if evs[len(evs)-1].Ev != "eof" {
		t.Errorf("last event %+v, want the process to see EOF", evs[len(evs)-1])
	}
	if _, err := os.Stat(start.Cwd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("process directory %s left behind: %v", start.Cwd, err)
	}
	if _, err := b.Load(context.Background(), pageA); !errors.Is(err, ErrBrowserClosed) {
		t.Errorf("Load after Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// A page that fails leaves the process in use, is never read after a failed
// navigation, and comes back as the right error.
func TestMCPPageFailuresKeepTheProcess(t *testing.T) {
	ctrl := "line1\nline2\x00\x1b[31m" + strings.Repeat("z", 5000)
	cases := []struct {
		name  string
		page  fakePage
		check func(error) bool
		read  bool // html is asked for
	}{
		{"404", fakePage{Status: 404}, func(e error) bool { return errors.Is(e, ErrHTTPStatus) && strings.Contains(e.Error(), "404") }, false},
		{"500", fakePage{Status: 503}, func(e error) bool { return errors.Is(e, ErrHTTPStatus) }, false},
		{"no status", fakePage{NoStatus: true}, func(e error) bool { return strings.Contains(e.Error(), "no HTTP status") }, false},
		{"robots", fakePage{NavFail: "RobotsBlocked"}, func(e error) bool { return strings.Contains(e.Error(), "RobotsBlocked") }, false},
		{"proxy down", fakePage{NavFail: "CouldntConnect"}, func(e error) bool { return strings.Contains(e.Error(), "CouldntConnect") }, false},
		{"too large upstream", fakePage{NavFail: "ResponseTooLarge"}, func(e error) bool { return strings.Contains(e.Error(), "ResponseTooLarge") }, false},
		{"nav timeout", fakePage{RPCCode: navTimeoutCode}, func(e error) bool { return errors.Is(e, ErrEmptyDocument) }, false},
		{"other rpc error", fakePage{RPCCode: -32000}, func(e error) bool { return !errors.Is(e, ErrEmptyDocument) && strings.Contains(e.Error(), "-32000") }, false},
		{"hostile message", fakePage{NavFail: ctrl}, func(e error) bool {
			m := e.Error()
			return len(m) < 2*stderrInError && !strings.ContainsAny(m, "\n\x00\x1b")
		}, false},
		{"html error", fakePage{HTMLFail: "boom"}, func(e error) bool { return strings.Contains(e.Error(), "boom") }, true},
		{"over the cap", fakePage{HTMLSize: MaxStdout + 1}, func(e error) bool { return errors.Is(e, ErrOutputTooLarge) }, true},
	}
	pages := map[string]fakePage{pageB: {HTML: "<p>fine</p>"}}
	for i, c := range cases {
		pages["https://careers.example.com/case/"+string(rune('a'+i))] = c.page
	}
	b, log := fakeLP(t, pages, 10*time.Second)
	for i, c := range cases {
		u := "https://careers.example.com/case/" + string(rune('a'+i))
		doc, err := b.Load(context.Background(), u)
		if err == nil || doc != nil || !c.check(err) {
			t.Errorf("%s: doc %d bytes, err %v", c.name, len(doc), err)
		}
		calls := toolCalls(t, log)
		if last := calls[len(calls)-1]; (last == "html") != c.read {
			t.Errorf("%s: last call %q; html asked = %v, want %v", c.name, last, last == "html", c.read)
		}
	}
	if got := mustLoad(t, b, pageB); got != "<p>fine</p>" {
		t.Errorf("good page after failures: %q", got)
	}
	if n := starts(t, log); n != 1 {
		t.Errorf("%d processes; page failures must not cost the process", n)
	}
}

// A document of exactly MaxStdout bytes is accepted.
func TestMCPDocumentAtTheCap(t *testing.T) {
	b, _ := fakeLP(t, map[string]fakePage{pageA: {HTMLSize: MaxStdout}}, 20*time.Second)
	if got := mustLoad(t, b, pageA); len(got) != MaxStdout {
		t.Fatalf("%d bytes", len(got))
	}
}

// A process that dies mid-page fails that page with its stderr's tail, and
// the next Load starts a new one.
func TestMCPCrashMidRunRestarts(t *testing.T) {
	b, log := fakeLP(t, map[string]fakePage{pageA: {Crash: true, Stderr: 1 << 20}, pageB: {HTML: "<p>b</p>"}}, 5*time.Second)
	_, err := b.Load(context.Background(), pageA)
	if !errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "fake crash marker") || len(err.Error()) > 2*stderrInError+200 {
		t.Fatalf("crash: %v (%d bytes)", err, len(err.Error()))
	}
	if got := mustLoad(t, b, pageB); got != "<p>b</p>" {
		t.Fatalf("after crash: %q", got)
	}
	if n := starts(t, log); n != 2 {
		t.Errorf("%d processes, want 2", n)
	}
}

// A browser that keeps dying is started at most 1+MaxRestarts times between
// Resets; past that Load fails without starting it. Reset restores the
// budget.
func TestMCPRestartCap(t *testing.T) {
	b, log := fakeLP(t, map[string]fakePage{pageA: {Crash: true}}, 5*time.Second)
	for i := range MaxRestarts + 3 {
		if _, err := b.Load(context.Background(), pageA); !errors.Is(err, ErrBrowserGone) {
			t.Fatalf("load %d: %v", i, err)
		}
	}
	if n := starts(t, log); n != 1+MaxRestarts {
		t.Fatalf("%d processes started, want %d", n, 1+MaxRestarts)
	}
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Load(context.Background(), pageA); !errors.Is(err, ErrBrowserGone) {
		t.Fatal(err)
	}
	if n := starts(t, log); n != 2+MaxRestarts {
		t.Errorf("%d processes after Reset, want %d", n, 2+MaxRestarts)
	}
}

// A hung page cannot be abandoned inside lightpanda, so its Load's own
// timeout, or the caller's context, kills the process promptly; the next
// Load starts a new one.
func TestMCPHungPage(t *testing.T) {
	b, log := fakeLP(t, map[string]fakePage{pageA: {Hang: true}, pageB: {HTML: "<p>b</p>"}}, 300*time.Millisecond)
	start := time.Now()
	if _, err := b.Load(context.Background(), pageA); err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Fatalf("hung page: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("hung page took %s", d)
	}
	if got := mustLoad(t, b, pageB); got != "<p>b</p>" {
		t.Fatalf("after hang: %q", got)
	}

	b2, log2 := fakeLP(t, map[string]fakePage{pageA: {Hang: true}, pageB: {HTML: "<p>b</p>"}}, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	if _, err := b2.Load(ctx, pageA); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("cancel took %s", d)
	}
	mustLoad(t, b2, pageB)
	if starts(t, log) != 2 || starts(t, log2) != 2 {
		t.Errorf("processes: %d and %d, want 2 each", starts(t, log), starts(t, log2))
	}
}

// A process that breaks the protocol is not trusted again: its page fails
// and the next Load starts a new process. Notifications and replies to
// nothing asked are skipped.
func TestMCPProtocolViolations(t *testing.T) {
	cases := map[string]struct {
		page fakePage
		want error
	}{
		"not json":        {fakePage{Junk: true}, ErrBrowserGone},
		"partial reply":   {fakePage{Partial: true}, ErrBrowserGone},
		"over-long line":  {fakePage{LongLine: maxLine + 1}, ErrOutputTooLarge},
		"chatter is fine": {fakePage{Chatter: 5, HTML: "<p>ok</p>"}, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			b, log := fakeLP(t, map[string]fakePage{pageA: c.page, pageB: {HTML: "<p>b</p>"}}, 10*time.Second)
			_, err := b.Load(context.Background(), pageA)
			if !errors.Is(err, c.want) {
				t.Fatalf("err %v, want %v", err, c.want)
			}
			mustLoad(t, b, pageB)
			want := 2
			if c.want == nil {
				want = 1
			}
			if n := starts(t, log); n != want {
				t.Errorf("%d processes, want %d", n, want)
			}
		})
	}

	// Unasked lines piling up past maxUnread end the process; the page they
	// followed was already read, and the next page is read by a new process.
	b, log := fakeLP(t, map[string]fakePage{pageA: {HTML: "<p>a</p>", Flood: maxUnread + 4}, pageB: {HTML: "<p>b</p>"}}, 5*time.Second)
	mustLoad(t, b, pageA)
	deadline := time.Now().Add(5 * time.Second)
	for {
		b.mu.Lock()
		dead := b.proc == nil || b.proc.dead()
		b.mu.Unlock()
		if dead || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mustLoad(t, b, pageB)
	if n := starts(t, log); n != 2 {
		t.Errorf("%d processes after a flood, want 2", n)
	}
}

// A process that cannot complete the handshake is not used.
func TestMCPStartFailures(t *testing.T) {
	b, _ := fakeLP(t, nil, 5*time.Second, "--fake-init=fail")
	if _, err := b.Load(context.Background(), pageA); !errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "initialize refused") {
		t.Errorf("refused handshake: %v", err)
	}
	b, _ = fakeLP(t, nil, 5*time.Second, "--fake-init=crash")
	if _, err := b.Load(context.Background(), pageA); !errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "fake init crash") {
		t.Errorf("crash in handshake: %v", err)
	}
	b, _ = fakeLP(t, nil, 300*time.Millisecond, "--fake-init=hang")
	if _, err := b.Load(context.Background(), pageA); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("hung handshake: %v", err)
	}

	missing, err := NewMCPBrowser("/nonexistent/lightpanda", nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Load(context.Background(), pageA); err == nil {
		t.Error("missing binary loaded a page")
	}
	if _, err := NewMCPBrowser("lightpanda", nil, time.Second); err == nil {
		t.Error("relative binary accepted: PATH lookup would be controlled by the environment")
	}
	if _, err := NewMCPBrowser("/opt/lp/lightpanda", nil, 0); err == nil {
		t.Error("zero timeout accepted")
	}
}

// Reset ends the process gracefully and the next Load starts a fresh one;
// Close during a hung Load ends it promptly.
func TestMCPResetAndClose(t *testing.T) {
	b, log := fakeLP(t, map[string]fakePage{pageA: {HTML: "<p>a</p>"}, pageB: {Hang: true}}, time.Minute)
	if err := b.Reset(); err != nil { // nothing started yet
		t.Fatal(err)
	}
	mustLoad(t, b, pageA)
	if err := b.Reset(); err != nil {
		t.Fatal(err)
	}
	if evs := events(t, log); evs[len(evs)-1].Ev != "eof" {
		t.Errorf("Reset did not end the process through its stdin: %+v", evs[len(evs)-1])
	}
	mustLoad(t, b, pageA)
	if n := starts(t, log); n != 2 {
		t.Errorf("%d processes, want 2", n)
	}

	done := make(chan error, 1)
	go func() {
		_, err := b.Load(context.Background(), pageB)
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hung Load succeeded after Close")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close left a Load hanging")
	}
	if d := time.Since(start); d > 2*stopWait+time.Second {
		t.Errorf("Close took %s", d)
	}
}

// Loads from several goroutines are serialized: lightpanda sees goto, html,
// goto, html, never two navigations in a row.
func TestMCPLoadsAreSerial(t *testing.T) {
	pages := map[string]fakePage{}
	var urls []string
	for i := range 6 {
		u := "https://careers.example.com/s/" + string(rune('a'+i))
		pages[u] = fakePage{HTML: "<p>" + u + "</p>", DelayMs: 20}
		urls = append(urls, u)
	}
	b, log := fakeLP(t, pages, 10*time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, len(urls))
	for _, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc, err := b.Load(context.Background(), u)
			if err == nil && string(doc) != "<p>"+u+"</p>" {
				err = errors.New("wrong document for " + u + ": " + string(doc))
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := toolCalls(t, log)
	if len(calls) != 2*len(urls) {
		t.Fatalf("%d calls: %q", len(calls), calls)
	}
	for i, c := range calls {
		if (i%2 == 0) != strings.HasPrefix(c, "goto ") {
			t.Fatalf("calls interleaved at %d: %q", i, calls)
		}
	}
}

// A Load waiting for another to finish gives up when its context ends.
func TestMCPQueuedLoadHonoursContext(t *testing.T) {
	b, _ := fakeLP(t, map[string]fakePage{pageA: {Hang: true}}, time.Minute)
	go func() { _, _ = b.Load(context.Background(), pageA) }()
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := b.Load(ctx, pageB); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued load: %v", err)
	}
}
