package doctor

import (
	"errors"
	"strings"
	"testing"
)

func TestDigestFoldsCountsAndIDsIntoOneLine(t *testing.T) {
	a := Log{Name: "a.log", Body: []byte(strings.Join([]string{
		`2026-11-10T14:35:15.1-06:00 board wd/snapchat: details {Listed:0 Queries:10 Requests:10}`,
		`2026-11-10T14:36:00Z workday "snapchat/snap": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		`2026-11-10T15:36:00Z workday "snapchat/snap": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		`all good here`,
	}, "\n"))}
	b := Log{Name: "b.log", Body: []byte(`2026-11-11 09:00:00 workday "snapchat/snap": context deadline exceeded (Client.Timeout exceeded while awaiting headers)` + "\n" +
		`gh pay request 0123456789abcdef0123 failed: HTTP 503`)}
	d := Digest([]Log{a, b, {Name: "gone.log", Err: errors.New("no such file")}}, nil)
	if d.LogsRead != 2 || len(d.Unreadable) != 1 || d.Unreadable[0] != "gone.log" {
		t.Fatalf("read %d, unreadable %v", d.LogsRead, d.Unreadable)
	}
	top := d.Lines[0]
	if top.Count != 3 || top.Logs != 2 || !strings.HasPrefix(top.Text, `workday "snapchat/snap": context deadline exceeded`) {
		t.Fatalf("top %+v", top)
	}
	var sawID bool
	for _, l := range d.Lines {
		if strings.Contains(l.Text, "all good") {
			t.Errorf("a line with no trouble word was kept: %q", l.Text)
		}
		if strings.Contains(l.Text, "<id>") && strings.Contains(l.Text, "HTTP 503") {
			sawID = true
		}
	}
	if !sawID {
		t.Errorf("hex id and status not folded: %+v", d.Lines)
	}
}

// A signal carries the raw listing line of the run that read nothing before
// the newest ones: that line is what separates an empty answer (Listed:0
// Prefiltered:0) from a board the owner's prefilter emptied (Listed:7
// Prefiltered:7).
func TestDetailsLeadWithTheAnomalousRun(t *testing.T) {
	h := runs("wd:snapchat/snap", 46, 46, 0, 44, 44, 44, 44)
	logs := []Log{} // newest first, named after their runs
	for i := len(h) - 1; i >= 0; i-- {
		listed := "46"
		if i == 2 {
			listed = "0"
		}
		logs = append(logs, Log{Name: h[i].RunID + ".log", Body: []byte(
			"vani: board wd/snapchat/snap: details {Listed:1 Queries:10}\n" + // superseded within the log
				"vani: board wd/snapchat/snap: details {Listed:" + listed + " Queries:10 Prefiltered:0}\n" +
				"vani: board gh/other: details {Listed:5}\n")})
	}
	sigs := Signals(h, nil)
	AttachDetails(sigs, Details(logs))
	s := signalOf(t, sigs, "wd:snapchat/snap")
	if len(s.Details) != MaxDetails || s.Details[0] != "r02.log: {Listed:0 Queries:10 Prefiltered:0}" ||
		s.Details[1] != "r06.log: {Listed:46 Queries:10 Prefiltered:0}" {
		t.Fatalf("details %q", s.Details)
	}
	// A board no log names has none; an unreadable log is skipped.
	AttachDetails(sigs, Details([]Log{{Name: "x", Err: errors.New("gone")}}))
	if len(signalOf(t, sigs, "wd:snapchat/snap").Details) != 0 {
		t.Fatal("details survived an empty read")
	}
}

// Lines shaped as the live run logs write them: the per-board summaries
// (whose Gone:0 and Stubs:0, and counts such as 461, once read as trouble)
// are not kept, and an HTTP status survives digit folding.
func TestDigestKeepsTroubleNotRoutine(t *testing.T) {
	body := strings.Join([]string{
		`vani: board wd/visa/Visa: details {Listed:461 Queries:10 Requests:13 Reused:42 Fetched:0 Refreshed:2 Prefiltered:25 Deferred:0 Gone:0 Stubs:0}`,
		`vani: board az/amazon: {New:2 Changed:0 Rerendered:0 Kept:0 Redated:0 Refreshed:1 Unchanged:457 Reappeared:0 Missed:1 Dead:2 Duplicate:0 Revived:0 Invalid:0}`,
		`vani: board gh/x: reading (80 of 451)`,
		`vani: eightfold "microsoft.com": rate limited by apply.careers.microsoft.com (HTTP 429), retry next run`,
		`vani: tb "citi": 6 of 53 job pages are gone (HTTP 404)`,
		`vani: gh pay request failed: status 503`,
		`vani: board wd/broadcom: 2 of 75 listing rows are stubs with no path or title`,
		`vani: board ab/airbyte: forms {Reused:12 Fetched:0 Unread:0 Prefiltered:3 Failed:0 NotFound:0 Skipped:0 FirstError:}`,
		`vani: board ab/clera: forms {Reused:2 Fetched:5 Unread:0 Prefiltered:0 Failed:2 NotFound:0 Skipped:0 FirstError:dial tcp: i/o timeout}`,
		`vani: logos: fetched 0, fresh 412, waiting 3, failed 0, deferred 0`,
		`vani: idle warm: scored 40, rescored 0, failed 3 in 2 chunk(s)`,
	}, "\n")
	d := Digest([]Log{{Name: "run.log", Body: []byte(body)}}, nil)
	var texts []string
	for _, l := range d.Lines {
		texts = append(texts, l.Text)
	}
	got := strings.Join(texts, "\n")
	for _, want := range []string{"(HTTP 429)", "(HTTP 404)", "status 503", "stubs with no path", "ab/clera: forms", "idle warm"} {
		if !strings.Contains(got, want) {
			t.Errorf("lost %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"details {", "{New:", "reading (N of N)", "ab/airbyte", "logos:"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept a routine line %q:\n%s", bad, got)
		}
	}
	if d.LinesSeen != 6 {
		t.Errorf("%d trouble lines, want 6:\n%s", d.LinesSeen, got)
	}
}

// The redactor runs before a line is kept, so nothing it removes is sent.
func TestDigestRedactsBeforeKeeping(t *testing.T) {
	d := Digest([]Log{{Name: "x", Body: []byte("request failed: Authorization: Bearer sk-secret-123")}}, func(s string) string {
		return strings.ReplaceAll(s, "sk-secret-123", "[redacted]")
	})
	if len(d.Lines) != 1 || strings.Contains(d.Lines[0].Text, "secret") || !strings.Contains(d.Lines[0].Text, "[redacted]") {
		t.Fatalf("%+v", d.Lines)
	}
}

// Adversarial logs: empty, a single huge line, a tail whose first line is
// partial, no final newline, more distinct lines than the cap.
func TestDigestBounds(t *testing.T) {
	if d := Digest(nil, nil); d.LogsRead != 0 || len(d.Lines) != 0 {
		t.Fatalf("%+v", d)
	}
	huge := strings.Repeat("error ", maxScanLine) // one line past the scan bound
	d := Digest([]Log{{Name: "h", Body: []byte(huge + "\nfailed after\n")}}, nil)
	if d.TooLong != 1 || len(d.Lines) != 1 || d.Lines[0].Text != "failed after" {
		t.Fatalf("huge line: %+v", d)
	}
	d = Digest([]Log{{Name: "t", Truncated: true, Body: []byte("rror half a line\nreal failure\nlast failure no newline")}}, nil)
	if len(d.TailOnlyFor) != 1 || len(d.Lines) != 2 {
		t.Fatalf("tail: %+v", d)
	}
	for _, l := range d.Lines {
		if strings.HasPrefix(l.Text, "rror") {
			t.Errorf("the partial first line of a tail was kept: %q", l.Text)
		}
	}
	var many []string
	for i := 0; i < MaxLogLines+15; i++ {
		many = append(many, "failed board "+strings.Repeat("x", i+1))
	}
	d = Digest([]Log{{Name: "m", Body: []byte(strings.Join(many, "\n"))}}, nil)
	if len(d.Lines) != MaxLogLines || d.LeftOut != 15 || d.Distinct != MaxLogLines+15 {
		t.Fatalf("cap: kept %d, left out %d, distinct %d", len(d.Lines), d.LeftOut, d.Distinct)
	}
	if n := len([]rune(Normalize(strings.Repeat("failed ", 200)))); n > maxLineRunes {
		t.Fatalf("normalised line is %d runes", n)
	}
}
