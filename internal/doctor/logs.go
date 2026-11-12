package doctor

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Bounds on the log digest.
const (
	// MaxLogBytes is how much of one log's tail is read.
	MaxLogBytes = 2 << 20
	// maxLineRunes bounds one normalised line.
	maxLineRunes = 220
	// MaxLogLines bounds the distinct lines kept across every log.
	MaxLogLines = 60
	// maxScanLine bounds one physical line; a longer one is skipped whole.
	maxScanLine = 64 << 10
)

// LogLine is one distinct trouble line and how often it occurred, across how
// many of the logs read.
type LogLine struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
	Logs  int    `json:"logs"`
}

// LogDigest is the distinct trouble lines of a set of logs, most frequent
// first, with how much was read and what was left out, so a capped digest is
// never read as the whole of the logs.
type LogDigest struct {
	Lines       []LogLine `json:"lines"`
	LogsRead    int       `json:"logs_read"`
	LinesSeen   int       `json:"lines_seen"`    // trouble lines before deduplication
	Distinct    int       `json:"distinct"`      // distinct after normalisation
	LeftOut     int       `json:"left_out"`      // distinct lines past MaxLogLines
	TooLong     int       `json:"too_long"`      // physical lines past maxScanLine, skipped
	Unreadable  []string  `json:"unreadable"`    // logs that could not be read, by name
	TailOnlyFor []string  `json:"tail_only_for"` // logs longer than MaxLogBytes, read from their tail
}

var (
	// troubleRE is a line worth reading. A status code counts only beside
	// "HTTP" or "status": a bare 404 or 461 is as likely a listing count.
	troubleRE = regexp.MustCompile(`(?i)\b(error|failed|failure|timeout|timed out|deadline|refused|denied|forbidden|rate limited|cooldown|robots|unexpected|stubs? with|not found|panic|truncat|incomplete)\b|\b(?:HTTP|status)[ :]*[45]\d\d\b`)
	// routineRE is a per-board summary line ("board gh/x: details {...}", the
	// reconcile counts "board gh/x: {New:0 ...}"): its zero counters (Gone:0,
	// Stubs:0) are not trouble, and boards carry these lines as raw_details.
	routineRE = regexp.MustCompile(`board \S+: (details )?\{[A-Za-z]+:`)
	// numRE folds counts; an HTTP status is matched whole and kept.
	numRE = regexp.MustCompile(`(?i)\b(?:HTTP|status)[ :]*[1-5]\d\d\b|\d+`)
	hexRE = regexp.MustCompile(`\b[0-9a-f]{12,}\b`)
	// timestamp prefixes, so the same event at two times is one line
	stampRE = regexp.MustCompile(`^\S*\d{4}[-/]\d{2}[-/]\d{2}[T ][\d:.]+\S*\s*`)
)

// Normalize is the dedupe form of a log line: its timestamp prefix removed,
// counts and long hex ids folded (an HTTP status is kept: a 429 and a 503 are
// different troubles), whitespace collapsed, cut to one bounded line. Two
// lines that differ only by a count or an id are one line.
func Normalize(line string) string {
	s := stampRE.ReplaceAllString(strings.TrimSpace(line), "")
	s = hexRE.ReplaceAllString(s, "<id>")
	s = numRE.ReplaceAllStringFunc(s, func(m string) string {
		if m[0] >= '0' && m[0] <= '9' {
			return "N"
		}
		return m
	})
	return cut(s, maxLineRunes)
}

var (
	// structRE is a counter dump ("forms {Reused:3 Failed:0 FirstError:}").
	structRE = regexp.MustCompile(`\{[A-Za-z]+:[^{}]*\}`)
	// badCounterRE is a dump that reports trouble: a failure counter above
	// zero, or a first error with a value.
	badCounterRE = regexp.MustCompile(`\b(Failed|Unread|NotFound|Gone|Stubs|Invalid|Errors?):[1-9]|\bFirstError:[^\s}]`)
	// zeroCountRE is prose that reports no trouble ("failed 0", "0 errors").
	zeroCountRE = regexp.MustCompile(`(?i)\b(failed|errors?|failures?)[ :=]+0\b|\b0 (failed|errors?|failures?)\b`)
)

// trouble is whether a log line is kept: a trouble word or an HTTP error
// status, outside the per-board summary lines. A counter dump is trouble only
// when it reports some (a failure counter above zero, a first error), and a
// zero count in prose ("failed 0") is not.
func trouble(line string) bool {
	if routineRE.MatchString(line) {
		return false
	}
	for _, d := range structRE.FindAllString(line, -1) {
		if badCounterRE.MatchString(d) {
			return true
		}
	}
	rest := zeroCountRE.ReplaceAllString(structRE.ReplaceAllString(line, ""), "")
	return troubleRE.MatchString(rest)
}

// Log is one log to digest: a name for the report and its content's tail.
type Log struct {
	Name string
	// Body is at most MaxLogBytes of the log; Truncated says the log was longer.
	Body      []byte
	Truncated bool
	// Err is why the log could not be read; Body is then empty.
	Err error
}

// Digest reads every log's trouble lines, normalises them with Normalize after
// redact (which must remove secrets and anything personal; nil is the
// identity), and keeps the MaxLogLines most frequent.
func Digest(logs []Log, redact func(string) string) LogDigest {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	type agg struct {
		count int
		logs  map[int]bool
	}
	seen := map[string]*agg{}
	var d LogDigest
	for i, l := range logs {
		if l.Err != nil {
			d.Unreadable = append(d.Unreadable, l.Name)
			continue
		}
		d.LogsRead++
		if l.Truncated {
			d.TailOnlyFor = append(d.TailOnlyFor, l.Name)
		}
		body := l.Body
		if l.Truncated {
			// The first line of a tail is likely partial.
			if j := bytes.IndexByte(body, '\n'); j >= 0 {
				body = body[j+1:]
			}
		}
		r := bufio.NewReaderSize(bytes.NewReader(body), 64<<10)
		for {
			line, tooLong, err := readLine(r)
			if tooLong {
				d.TooLong++
			} else if line != "" && trouble(line) {
				d.LinesSeen++
				key := Normalize(redact(line))
				a := seen[key]
				if a == nil {
					a = &agg{logs: map[int]bool{}}
					seen[key] = a
				}
				a.count++
				a.logs[i] = true
			}
			if err != nil {
				break
			}
		}
	}
	for text, a := range seen {
		d.Lines = append(d.Lines, LogLine{Text: text, Count: a.count, Logs: len(a.logs)})
	}
	sort.Slice(d.Lines, func(i, j int) bool {
		if d.Lines[i].Count != d.Lines[j].Count {
			return d.Lines[i].Count > d.Lines[j].Count
		}
		return d.Lines[i].Text < d.Lines[j].Text
	})
	d.Distinct = len(d.Lines)
	if len(d.Lines) > MaxLogLines {
		d.LeftOut = len(d.Lines) - MaxLogLines
		d.Lines = d.Lines[:MaxLogLines]
	}
	sort.Strings(d.Unreadable)
	sort.Strings(d.TailOnlyFor)
	return d
}

// detailsRE is a run log's per-board listing line:
// "vani: board wd/snapchat/snap: details {Listed:0 Queries:10 ...}".
var detailsRE = regexp.MustCompile(`board ([a-z]{2,4})/(\S+?): details (\{[^{}]{0,400}\})`)

// MaxDetails bounds the raw details lines kept per board.
const MaxDetails = 3

// reconcileRE is a run log's per-board reconcile line, written for every
// board the run read, including those its record leaves unnamed:
// "vani: board ab/Clera: {New:30 Changed:0 ... Missed:20 Dead:0 ...}".
var (
	reconcileRE = regexp.MustCompile(`board ([a-z]{2,4})/(\S+?): (\{New:\d+[^{}]{0,400}\})`)
	countRE     = regexp.MustCompile(`([A-Za-z]+):(\d{1,9})`)
)

// LogBoardRuns is the boards one run's own log says it read, as BoardRuns of
// that run, for the boards its record left unnamed. A board's listed count is
// every reconcile count but Missed and Dead (the postings absent from the
// listing), which is how the record counts it: checked against the record on
// 2026-11-12 for boards it does name (670 = Unchanged 670; 461 = New 2 +
// Refreshed 1 + Revived 1 + Unchanged 457). Within a log a board's last line
// is its own.
func LogBoardRuns(l Log, runID string, at time.Time) []BoardRun {
	if l.Err != nil {
		return nil
	}
	last := map[string]string{}
	var order []string
	for _, m := range reconcileRE.FindAllSubmatch(l.Body, -1) {
		b := string(m[1]) + ":" + string(m[2])
		if _, seen := last[b]; !seen {
			order = append(order, b)
		}
		last[b] = string(m[3])
	}
	out := make([]BoardRun, 0, len(order))
	for _, b := range order {
		br := BoardRun{RunID: runID, At: at, Board: b, Outcome: OutcomeRead, FromLog: true}
		listed := 0
		for _, c := range countRE.FindAllStringSubmatch(last[b], -1) {
			n, err := strconv.Atoi(c[2])
			if err != nil {
				continue
			}
			switch c[1] {
			case "Missed":
				br.Missed = n
			case "Dead":
				br.Dead = n
			case "New":
				br.New = n
				listed += n
			default:
				listed += n
			}
		}
		br.Listed = &listed
		out = append(out, br)
	}
	return out
}

// DetailLine is one board's raw listing line in one log.
type DetailLine struct {
	Log  string // the log's name; a run's own log is "<run id>.log"
	Line string
}

// Details is each board's raw listing lines ("ats:token" -> lines), from logs
// given newest first, newest first: within a log the last line for a board is
// its own. They carry what the run record folds away: the listing before the
// board's prefilter, the prefiltered count, queries and requests.
func Details(logs []Log) map[string][]DetailLine {
	out := map[string][]DetailLine{}
	for _, l := range logs {
		if l.Err != nil {
			continue
		}
		last := map[string]string{}
		for _, m := range detailsRE.FindAllSubmatch(l.Body, -1) {
			last[string(m[1])+":"+string(m[2])] = string(m[3])
		}
		for board, line := range last {
			out[board] = append(out[board], DetailLine{Log: l.Name, Line: line})
		}
	}
	return out
}

// AttachDetails sets each signal's Details from d (Details' result): the
// lines of its anomalous runs first (the reads that listed nothing or marked
// many missed, newest first), then its newest lines, at most MaxDetails.
func AttachDetails(sigs []BoardSignal, d map[string][]DetailLine) {
	for i := range sigs {
		s := &sigs[i]
		s.Details = nil
		lines := d[s.Board]
		taken := make([]bool, len(lines))
		take := func(j int) {
			if !taken[j] && len(s.Details) < MaxDetails {
				taken[j] = true
				s.Details = append(s.Details, lines[j].Log+": "+lines[j].Line)
			}
		}
		for k := len(s.anomalous) - 1; k >= 0; k-- {
			for j, l := range lines {
				if l.Log == s.anomalous[k]+".log" {
					take(j)
				}
			}
		}
		for j := range lines {
			take(j)
		}
	}
}

// readLine reads one line of at most maxScanLine bytes. A longer line is
// consumed and reported tooLong. err is io.EOF (or a read error) after the
// last line.
func readLine(r *bufio.Reader) (line string, tooLong bool, err error) {
	var b []byte
	for {
		chunk, isPrefix, e := r.ReadLine()
		if len(b)+len(chunk) > maxScanLine {
			tooLong = true
		} else {
			b = append(b, chunk...)
		}
		if e != nil {
			if e == io.EOF && (len(b) > 0 || tooLong) {
				return lineOrNone(b, tooLong), tooLong, nil
			}
			return lineOrNone(b, tooLong), tooLong, e
		}
		if !isPrefix {
			return lineOrNone(b, tooLong), tooLong, nil
		}
	}
}

func lineOrNone(b []byte, tooLong bool) string {
	if tooLong {
		return ""
	}
	return string(b)
}
