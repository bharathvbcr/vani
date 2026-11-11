package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
)

// jobsServeOnce runs a server and sends it the request lines one at a time,
// each after the previous one's answer (every non-blank line is answered
// exactly once), then closes its input. It returns the responses by id (and
// the ones without an id), failing on an id answered twice.
func jobsServeOnce(t *testing.T, e *env, lines ...string) (map[string]serveResponse, []serveResponse) {
	t.Helper()
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	pr, pw := io.Pipe()
	out := newLineReader(t)
	s := newJobsServer(e, res, out.w)
	done := make(chan error, 1)
	go func() { done <- s.serve(pr) }()
	var all bytes.Buffer
	for _, l := range lines {
		fmt.Fprintln(pw, l)
		all.WriteString(out.nextRaw(t) + "\n") // the line as written: re-encoding would change its bytes
	}
	pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return parseResponses(t, all.String())
}

func parseResponses(t *testing.T, out string) (map[string]serveResponse, []serveResponse) {
	t.Helper()
	byID := map[string]serveResponse{}
	var anon []serveResponse
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var r serveResponse
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("response %q: %v", l, err)
		}
		if r.ID == "" {
			anon = append(anon, r)
			continue
		}
		if _, dup := byID[r.ID]; dup {
			t.Fatalf("id %s answered twice", r.ID)
		}
		byID[r.ID] = r
	}
	return byID, anon
}

func req(id, op string, args ...string) string {
	b, _ := json.Marshal(serveRequest{ID: id, Op: op, Params: serveJSON(serveArgs{Args: args})})
	return string(b)
}

func serveJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Every view serve answers is the command's reply byte for byte; bad requests
// are refused as values and the session answers what follows them.
func TestJobsServeAnswersAsTheCommand(t *testing.T) {
	e, seeds := factsFixture(t, config.Exclude{Titles: []string{"intern"}})
	cases := []struct {
		id, op string
		args   []string
	}{
		{"1", "list", []string{"--view", "new"}},
		{"2", "list", []string{"--view", "all", "--sort", "company", "--limit", "5"}},
		{"3", "list", []string{"--view", "pending"}},
		{"4", "stats", []string{"--view", "new"}},
		{"5", "gaps", []string{"--view", "all"}},
		{"6", "show", []string{seeds[0].key}},
		{"7", "list", []string{"--view", "saved", "--sort", "salary"}},
	}
	var lines []string
	for _, c := range cases {
		lines = append(lines, req(c.id, c.op, c.args...))
	}
	huge := `{"id":"big","op":"list","params":{"args":["` + strings.Repeat("x", serveMaxLine) + `"]}}`
	lines = append(lines,
		`{"id":"h","op":"hello","params":{"protocol":1}}`,
		`not json`,
		`{"op":"list"}`,
		req("u", "mark", "gh:co1:01", "saved"),
		req("j", "list", "--json=false"),
		req("f", "list", "--no-such-flag"),
		req("nf", "show", "gh:nope:0"),
		huge,
		req("after", "list", "--view", "hidden"),
	)
	got, anon := jobsServeOnce(t, e, lines...)

	for _, c := range cases {
		r := got[c.id]
		if !r.OK {
			t.Fatalf("%s %v: %+v", c.op, c.args, r.Error)
		}
		if want := cliReply(t, e, serveOps[c.op], c.args...); string(r.Result) != want {
			t.Errorf("%s %v: serve and the command differ: %s", c.op, c.args, firstDiff(string(r.Result), want))
		}
	}
	var h serveHello
	if !got["h"].OK || json.Unmarshal(got["h"].Result, &h) != nil || h.Protocol != jobsServeProtocol || len(h.Ops) != 5 ||
		h.OpLimits["list"].DeadlineMS != serveDeadline.Milliseconds() {
		t.Errorf("hello: %+v %s", got["h"], got["h"].Result)
	}
	for id, code := range map[string]string{"u": serveErrBadRequest, "j": serveErrBadRequest, "f": serveErrBadRequest,
		"nf": serveErrCommand, "big": serveErrTooLarge} {
		if r := got[id]; r.OK || r.Error == nil || r.Error.Code != code {
			t.Errorf("%s: %+v, want %s", id, r.Error, code)
		}
	}
	if r := got["nf"]; r.Error != nil && (len(r.Error.Reply) == 0 || string(r.Error.Reply) != cliReply(t, e, (*env).jobsShow, "gh:nope:0")) {
		t.Errorf("a failed show: reply %s, the command's %s", r.Error.Reply, cliReply(t, e, (*env).jobsShow, "gh:nope:0"))
	}
	if len(anon) != 2 {
		t.Errorf("%d responses without an id, want 2 (not json, no id): %+v", len(anon), anon)
	}
	if !got["after"].OK {
		t.Errorf("the request after the refusals: %+v", got["after"].Error)
	}
}

// Requests computing at once over the one resident Reader and facts store
// (serve runs up to serveMaxRunning) each answer the command's bytes: each
// works on its own copy of the snapshot, and the facts they derive, put and
// prune at the same time from an empty store do not leak between them. Run
// with -race.
func TestJobsServeConcurrentOpsAnswerAsTheCommand(t *testing.T) {
	e, seeds := factsFixture(t, config.Exclude{Titles: []string{"intern"}})
	cases := []struct {
		op   string
		args []string
	}{
		{"list", []string{"--view", "new"}},
		{"list", []string{"--view", "all", "--sort", "salary"}},
		{"stats", []string{"--view", "new"}},
		{"gaps", []string{"--view", "all"}},
		{"show", []string{seeds[0].key}},
		{"list", []string{"--view", "new", "--q", "go"}},
	}
	want := make([]string, len(cases))
	for i, c := range cases {
		want[i] = cliReply(t, e, serveOps[c.op], c.args...)
	}
	// Serve starts with no stored facts, so the first requests derive and save
	// them while the others read.
	if err := os.Remove(e.jobsFactsPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)

	const rounds = 4
	var wg sync.WaitGroup
	errs := make(chan string, rounds*len(cases))
	for round := 0; round < rounds; round++ {
		for i, c := range cases {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := s.runOp(fmt.Sprintf("%d-%d", round, i), serveOps[c.op], c.args)
				switch {
				case !r.OK:
					errs <- fmt.Sprintf("%s %v: %+v", c.op, c.args, r.Error)
				case string(r.Result) != want[i]:
					errs <- fmt.Sprintf("%s %v: serve and the command differ: %s", c.op, c.args, firstDiff(string(r.Result), want[i]))
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Error(msg)
	}
}

// A request reads the ledger as it is now: lines appended and marks written
// after serve started are in the next reply, as in the command's.
func TestJobsServeReadsTheLatestState(t *testing.T) {
	e, seeds := factsFixture(t, config.Exclude{})
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	pr, pw := io.Pipe()
	out := newLineReader(t)
	s := newJobsServer(e, res, out.w)
	done := make(chan error, 1)
	go func() { done <- s.serve(pr) }()

	ask := func(id string, args ...string) json.RawMessage {
		t.Helper()
		fmt.Fprintln(pw, req(id, "list", args...))
		r := out.next(t)
		if r.ID != id || !r.OK {
			t.Fatalf("%s: %+v", id, r)
		}
		return r.Result
	}
	ask("1", "--view", "all")
	changed := seeds[0]
	changed.desc = "Now Haskell only. Pay: $300,000 - $350,000 per year."
	changed.title = "Haskell Engineer"
	appendPosting(t, e.stateDir, changed)
	mustCode(t, 0, "jobs", "mark", seeds[1].key, "hidden")
	got := ask("2", "--view", "all")
	if want := cliReply(t, e, (*env).jobsList, "--view", "all"); string(got) != want {
		t.Fatalf("after an append and a mark, serve and the command differ\nserve %.300s\n  cli %.300s", got, want)
	}
	if !strings.Contains(string(got), "Haskell Engineer") {
		t.Fatal("the appended line is not in the reply")
	}
	pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// lineReader collects a server's output lines as they are written.
type lineReader struct {
	w     *io.PipeWriter
	lines chan string
}

func newLineReader(t *testing.T) *lineReader {
	pr, pw := io.Pipe()
	l := &lineReader{w: pw, lines: make(chan string, 64)}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			l.lines <- sc.Text()
		}
		close(l.lines)
	}()
	t.Cleanup(func() { pw.Close() })
	return l
}

func (l *lineReader) next(t *testing.T) serveResponse {
	t.Helper()
	s := l.nextRaw(t)
	var r serveResponse
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return r
}

// nextRaw is the next response line as the server wrote it.
func (l *lineReader) nextRaw(t *testing.T) string {
	t.Helper()
	select {
	case s, ok := <-l.lines:
		if !ok {
			t.Fatal("the server closed its output")
		}
		return s
	case <-time.After(30 * time.Second):
		t.Fatal("no response")
	}
	return ""
}

// fakeOps replaces the served ops for one test with ones that block until
// released (or panic), so the order of answers is under the test's control.
func fakeOps(t *testing.T) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	old := serveOps
	block := func(e *env, args []string) error {
		<-gate
		fmt.Fprintln(e.stdout, `{"ok":"done"}`)
		return nil
	}
	serveOps = map[string]func(*env, []string) error{
		"list": block, "stats": block, "gaps": block,
		"show": func(*env, []string) error { panic("boom") },
	}
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() { release(); serveOps = old })
	return release
}

// A newer list answers the older one superseded at once; past the running
// bound a request is refused busy; a request with no answer by the deadline is
// answered timed out; a panic is an internal error; stdin closing waits for
// every accepted request's one answer.
func TestJobsServeBounds(t *testing.T) {
	e, _ := factsFixture(t, config.Exclude{})
	release := fakeOps(t)
	res, err := e.openJobsResident()
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	pr, pw := io.Pipe()
	out := newLineReader(t)
	s := newJobsServer(e, res, out.w)
	s.deadline = 2 * time.Second
	done := make(chan error, 1)
	go func() { done <- s.serve(pr) }()
	send := func(lines ...string) {
		for _, l := range lines {
			fmt.Fprintln(pw, l)
		}
	}

	send(req("p", "show", "k"))
	if r := out.next(t); r.ID != "p" || r.Error == nil || r.Error.Code != serveErrInternal {
		t.Fatalf("panic: %+v", r)
	}
	send(req("a", "list"), req("b", "list"))
	if r := out.next(t); r.ID != "a" || r.Error == nil || r.Error.Code != serveErrSuperseded {
		t.Fatalf("superseded: %+v", r)
	}
	send(req("c", "stats"), req("d", "gaps"))
	// a (superseded, its work still running), b, c, d: four running, so the
	// next request is refused, and refused without superseding b.
	send(req("busy", "list"))
	if r := out.next(t); r.ID != "busy" || r.Error == nil || r.Error.Code != serveErrBusy || !r.Error.Retryable {
		t.Fatalf("busy: %+v", r)
	}
	timedOut := map[string]bool{}
	for range 3 {
		r := out.next(t)
		if r.Error == nil || r.Error.Code != serveErrTimeout || !r.Error.Retryable {
			t.Fatalf("timeout: %+v", r)
		}
		timedOut[r.ID] = true
	}
	if !timedOut["b"] || !timedOut["c"] || !timedOut["d"] {
		t.Fatalf("timed out: %v", timedOut)
	}
	release()
	// Their work ends now; the answers are already given and nothing more is
	// written for them. A new request is answered.
	// The released requests leave the running count as they end; until then a
	// new one is refused busy, retryably, which is what a host retries on.
	for i := 0; ; i++ {
		id := fmt.Sprintf("f%d", i)
		send(req(id, "list"))
		r := out.next(t)
		if r.ID != id {
			t.Fatalf("after release: %+v", r)
		}
		if r.OK {
			break
		}
		if r.Error.Code != serveErrBusy || i == 100 {
			t.Fatalf("after release: %+v", r.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Closing stdin with a request running waits for its answer.
	gate := make(chan struct{})
	serveOps["list"] = func(e *env, _ []string) error { <-gate; fmt.Fprintln(e.stdout, `{}`); return nil }
	send(req("last", "list"))
	pw.Close()
	select {
	case <-done:
		t.Fatal("serve returned before answering an accepted request")
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	if r := out.next(t); r.ID != "last" || !r.OK {
		t.Fatalf("last: %+v", r)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A line past the cap is refused, with its id when the head carries one, and
// the line after it is read whole.
func TestReadServeLine(t *testing.T) {
	big := `{"id": "big-1","op":"list","x":"` + strings.Repeat("y", 3*serveMaxLine) + `"}`
	in := strings.Join([]string{`{"id":"a"}`, big, `{"id":"b"}`, strings.Repeat("z", serveMaxLine)}, "\n") + "\n" + `{"id":"tail"}`
	br := bufio.NewReaderSize(strings.NewReader(in), serveMaxLine)
	want := []string{`{"id":"a"}`, "big-1", `{"id":"b"}`, strings.Repeat("z", serveMaxLine), `{"id":"tail"}`}
	for i, w := range want {
		line, err := readServeLine(br)
		var tl lineTooLarge
		switch {
		case w == "big-1":
			if !errorsAs(err, &tl) || recoverServeID(tl.head) != "big-1" {
				t.Fatalf("%d: %v, id %q", i, err, recoverServeID(tl.head))
			}
		case err != nil || string(line) != w:
			t.Fatalf("%d: %.40q, %v; want %.40q", i, line, err, w)
		}
	}
	if _, err := readServeLine(br); err != io.EOF {
		t.Fatalf("after the last line: %v", err)
	}
	if !isJSONFlag("--json") || !isJSONFlag("-json=false") || isJSONFlag("--jsonl") || isJSONFlag("json") {
		t.Fatal("isJSONFlag")
	}
}

// firstDiff shows where a and b first differ, with some context.
func firstDiff(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(i-120, 0)
	return fmt.Sprintf("at byte %d of %d/%d\nserve …%s\n  cli …%s", i, len(a), len(b), a[from:min(i+80, len(a))], b[from:min(i+80, len(b))])
}

func errorsAs(err error, target *lineTooLarge) bool {
	tl, ok := err.(lineTooLarge)
	if ok {
		*target = tl
	}
	return ok
}
