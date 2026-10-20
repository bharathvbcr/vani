package digest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func rec(key string, st posting.Status, title string, ann map[string]string) ledger.Record {
	return ledger.Record{
		Key:    posting.Key(key),
		Status: st,
		Posting: posting.Posting{
			Key: posting.Key(key), Company: "Acme", Title: title,
			Location: "Berlin", URL: "https://example.com/jobs/" + key,
		},
		Annotations: ann,
	}
}

var day = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

func TestSelectIncludeExcludeAndOrder(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", posting.Digested, "Already", map[string]string{"rank": "99"}),
		rec("gh:a:2", posting.Dropped, "DroppedRole", nil),
		rec("gh:a:3", posting.Dead, "Dead", nil),
		rec("gh:a:4", posting.Scored, "Not yet explained", nil),
		rec("gh:a:5", posting.Explained, "Low", map[string]string{"rank": "10.5"}),
		rec("gh:a:6", posting.Explained, "High", map[string]string{"rank": "88"}),
		rec("gh:a:7", posting.Explained, "NoScore", nil),
		rec("gh:a:8", posting.Explained, "BadScore", map[string]string{"rank": "abc"}),
		rec("gh:a:9", posting.Unsure, "UnsureHigh", map[string]string{"rank": "95"}),
		// Unsure at triage, then scored and explained: still belongs with the unsure ones.
		rec("gh:a:10", posting.Explained, "WasUnsure", map[string]string{"rank": "99", "triage.unsure": `slot "sponsorship" abstained`}),
	}
	md, keys := Render(recs, nil, day, Counts{New: 5, Dropped: 2, Dead: 1})
	want := []posting.Key{"gh:a:6", "gh:a:5", "gh:a:7", "gh:a:8", "gh:a:10", "gh:a:9"}
	if len(keys) != len(want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys %v, want %v", keys, want)
		}
	}
	for _, bad := range []string{"Already", "DroppedRole", "Not yet explained"} {
		if strings.Contains(md, bad) {
			t.Errorf("digest contains excluded %q", bad)
		}
	}
	if !strings.Contains(md, "New today: 5 | Dropped today: 2 | Dead today: 1") {
		t.Errorf("footer missing:\n%s", md)
	}
	if strings.Index(md, "High") > strings.Index(md, "UnsureHigh") {
		t.Error("explained must precede unsure regardless of score")
	}
	if strings.Contains(md, "Awaiting triage") {
		t.Error("footer names a backlog that is not there")
	}
}

// Options.Rules demote a role whose answered triage reading matches, and
// Options.TitleRank raises an included title, as in `vani jobs`; neither drops
// a role (Rule 7).
func TestRulesAndTitleRankReorder(t *testing.T) {
	ann := func(seniority string) map[string]string {
		return map[string]string{
			"rank": "60", "triage.role_fit": "strong_fit", "triage.role_fit.reading": "answered",
			"triage.seniority": seniority, "triage.seniority.reading": "answered", "triage.seniority.p": "0.95",
		}
	}
	recs := []ledger.Record{
		rec("gh:a:1", posting.Explained, "Senior Engineer", ann("senior")),
		rec("gh:a:2", posting.Explained, "Engineer", ann("mid")),
	}
	order := func(o Options) string {
		_, keys := RenderWith(recs, nil, day, Counts{}, o)
		var s []string
		for _, k := range keys {
			s = append(s, string(k))
		}
		return strings.Join(s, " ")
	}
	if got := order(Options{}); got != "gh:a:1 gh:a:2" {
		t.Fatalf("no rules: %s, want the key order of a tie", got)
	}
	rules := []jobinsight.DropRule{{Slot: "seniority", Values: []string{"senior"}}}
	if got := order(Options{Rules: rules}); got != "gh:a:2 gh:a:1" {
		t.Fatalf("seniority penalty: %s, want the senior role second and still listed", got)
	}
	title := func(s string) int {
		if strings.HasPrefix(s, "Senior") {
			return 1
		}
		return 0
	}
	if got := order(Options{TitleRank: title}); got != "gh:a:1 gh:a:2" {
		t.Fatalf("title rank: %s", got)
	}
	if got := order(Options{TitleRank: func(s string) int { return map[bool]int{true: 1}[s == "Engineer"] }}); got != "gh:a:2 gh:a:1" {
		t.Fatalf("title rank on the second role: %s, want it first", got)
	}
}

// Postings still awaiting triage are not in the digest, so the footer says
// how many there are, under the empty-digest line too.
func TestFooterSaysWhatAwaitsTriage(t *testing.T) {
	for _, recs := range [][]ledger.Record{nil, {rec("gh:a:1", posting.Explained, "One", map[string]string{"rank": "50"})}} {
		md, _ := Render(recs, nil, day, Counts{New: 3027, AwaitingTriage: 2639})
		if !strings.Contains(md, "Awaiting triage: 2639 (not in this digest") {
			t.Fatalf("footer:\n%s", md)
		}
	}
}

// A run with scoring off (no devprism.binary) explains postings that carry no
// rank. They must not be headed as ranked: that is a skipped stage passing for
// a ranking.
func TestUnscoredRolesAreNotHeadedRanked(t *testing.T) {
	unscored := []ledger.Record{
		rec("gh:a:1", posting.Explained, "One", map[string]string{"explanation": "x"}),
		rec("gh:a:2", posting.Explained, "Two", map[string]string{"rank": "", "explanation": "y"}),
	}
	md, keys := Render(unscored, nil, day, Counts{})
	if len(keys) != 2 {
		t.Fatalf("keys %v, want both unscored roles", keys)
	}
	if strings.Contains(md, "Ranked roles") {
		t.Errorf("unscored roles headed as ranked:\n%s", md)
	}
	if !strings.Contains(md, "## Not ranked (2)") {
		t.Errorf("missing not-ranked heading:\n%s", md)
	}

	mixed := append(unscored, rec("gh:a:3", posting.Explained, "Three", map[string]string{"rank": "70"}))
	md, keys = Render(mixed, nil, day, Counts{})
	want := []posting.Key{"gh:a:3", "gh:a:1", "gh:a:2"}
	for i := range want {
		if len(keys) != len(want) || keys[i] != want[i] {
			t.Fatalf("keys %v, want %v", keys, want)
		}
	}
	ranked, notRanked := strings.Index(md, "## Ranked roles (1)"), strings.Index(md, "## Not ranked (2)")
	if ranked < 0 || notRanked < 0 || ranked > notRanked {
		t.Errorf("want Ranked roles (1) before Not ranked (2):\n%s", md)
	}
	if strings.Index(md, "Three") > notRanked {
		t.Errorf("ranked role rendered under the not-ranked heading:\n%s", md)
	}
}

func TestEmptyDigest(t *testing.T) {
	md, keys := Render(nil, nil, day, Counts{})
	if len(keys) != 0 || !strings.Contains(md, "No new roles today.") {
		t.Fatalf("empty digest wrong: %q %v", md, keys)
	}
}

func TestEscapingMaliciousTitle(t *testing.T) {
	r := rec("gh:a:1", posting.Explained, "[evil](http://x)\n# heading <script>", map[string]string{
		"explanation": "Great.\n\n# Injected\n[click](javascript:alert(1)) ![img](http://x/y.png)",
	})
	r.Posting.Company = "Co [x](http://y)"
	md, _ := Render([]ledger.Record{r}, nil, day, Counts{})
	for _, bad := range []string{"[evil](", "\n# heading", "\n# Injected", "](javascript", "<script>", "![img]("} {
		if strings.Contains(md, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, md)
		}
	}
	if !strings.Contains(md, `\[evil\]\(http\://x\)`) {
		t.Errorf("escaped title not found:\n%s", md)
	}
	// Exactly one H1 and one H3 line: nothing injected a heading.
	h1, h3 := 0, 0
	for _, l := range strings.Split(md, "\n") {
		if strings.HasPrefix(l, "# ") {
			h1++
		}
		if strings.HasPrefix(l, "### ") {
			h3++
		}
		if strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "# Vani") && !strings.HasPrefix(l, "## ") && !strings.HasPrefix(l, "### ") {
			t.Errorf("stray heading line %q", l)
		}
	}
	if h1 != 1 || h3 != 1 {
		t.Errorf("h1=%d h3=%d", h1, h3)
	}
}

func TestBadURLsNotLinked(t *testing.T) {
	for _, u := range []string{"javascript:alert(1)", "data:text/html,x", "https://a.com/x> [y](z)", "", "https://a.com/ b"} {
		r := rec("gh:a:1", posting.Explained, "T", nil)
		r.Posting.URL = u
		md, _ := Render([]ledger.Record{r}, nil, day, Counts{})
		if !strings.Contains(md, "(no valid link)") {
			t.Errorf("url %q was linked:\n%s", u, md)
		}
	}
}

func TestUnsureReasonsAndTriage(t *testing.T) {
	r := rec("gh:a:1", posting.Unsure, "Engineer", map[string]string{
		"triage.sponsorship.reading": "unclear",
		"triage.work_mode.reading":   "maybe hybrid",
		"triage.role_fit":            "good",
		"triage.seniority":           "senior",
		"rank":                       "42",
		"gap.coverage_pct":           "60",
	})
	r.Posting.Remote = true
	md, _ := Render([]ledger.Record{r}, nil, day, Counts{})
	for _, want := range []string{
		"Why unsure: sponsorship read as unclear; work\\_mode read as maybe hybrid",
		// Coverage is gap.coverage_pct, not the rank (which may carry an
		// employer-preference boost).
		"- role_fit: good", "- seniority: senior", "Must-have coverage: 60%", "- Where: Berlin · Remote",
		"<https://example.com/jobs/gh:a:1>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	empty := rec("gh:a:2", posting.Unsure, "X", nil)
	md, _ = Render([]ledger.Record{empty}, nil, day, Counts{})
	if !strings.Contains(md, "no triage reading recorded") {
		t.Errorf("unsure without readings must say so:\n%s", md)
	}
}

func TestEscapeBounds(t *testing.T) {
	got := Escape(strings.Repeat("a", 1000), 10)
	if len([]rune(got)) != 11 || !strings.HasSuffix(got, "…") {
		t.Errorf("not truncated: %q", got)
	}
	if got := Escape("a\x00\r\nb\t c", 50); got != "a b c" {
		t.Errorf("whitespace not flattened: %q", got)
	}
}

func TestWriteAtomicPathAndKeys(t *testing.T) {
	dir := t.TempDir()
	recs := []ledger.Record{rec("gh:a:1", posting.Explained, "T", nil), rec("gh:a:2", posting.Live, "L", nil)}
	path, keys, err := Write(dir, day, recs, nil, Counts{New: 1})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "digests", "2026-10-18.md") {
		t.Errorf("path %s", path)
	}
	if len(keys) != 1 || keys[0] != "gh:a:1" {
		t.Errorf("keys %v", keys)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "# Vani digest 2026-10-18") {
		t.Fatalf("file wrong: %v %q", err, data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
	// Same day again with new roles: the first file is kept, a -2 file is added.
	p2, k2, err := Write(dir, day, []ledger.Record{rec("gh:a:3", posting.Explained, "Later", nil)}, nil, Counts{})
	if err != nil || p2 != filepath.Join(dir, "digests", "2026-10-18-2.md") || len(k2) != 1 {
		t.Fatalf("second digest: %s %v %v", p2, k2, err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "at Acme") || strings.Contains(string(data), "Later") {
		t.Error("first digest was overwritten")
	}
	// Same day with nothing new: nothing written, existing path returned.
	p3, k3, err := Write(dir, day, nil, nil, Counts{})
	if err != nil || p3 != path || len(k3) != 0 {
		t.Fatalf("empty rerun: %s %v %v", p3, k3, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 2 {
		t.Errorf("files: %v", entries)
	}
	if _, _, err := Write("", day, nil, nil, Counts{}); err == nil {
		t.Error("empty state dir accepted")
	}
}
