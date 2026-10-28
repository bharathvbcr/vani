package digest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var quietNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func applied(key string, stage marks.Stage, ago time.Duration) (posting.Key, marks.Entry) {
	at := quietNow.Add(-ago)
	e := marks.Entry{Key: posting.Key(key), Mark: marks.Applied, MarkedAt: at}
	if stage != marks.StageNone {
		e.Stage, e.StageAt = stage, at
	}
	return posting.Key(key), e
}

func quietMD(recs []ledger.Record, es ...func() (posting.Key, marks.Entry)) string {
	m := map[posting.Key]marks.Entry{}
	for _, f := range es {
		k, e := f()
		m[k] = e
	}
	md, _ := RenderWith(recs, nil, quietNow, Counts{}, Options{Quiet: &Quiet{Applied: m}})
	return md
}

func ent(key string, stage marks.Stage, ago time.Duration) func() (posting.Key, marks.Entry) {
	return func() (posting.Key, marks.Entry) { return applied(key, stage, ago) }
}

const dayLen = 24 * time.Hour

func TestQuietBoundaryIsWholeDays(t *testing.T) {
	recs := []ledger.Record{rec("gh:a:1", posting.Digested, "Edge", nil)}
	for name, c := range map[string]struct {
		ago  time.Duration
		want bool
	}{
		"exactly N days":     {QuietDays * dayLen, true},
		"a second short":     {QuietDays*dayLen - time.Second, false},
		"N-1 days":           {(QuietDays - 1) * dayLen, false},
		"N days and a bit":   {QuietDays*dayLen + 5*time.Hour, true},
		"just now":           {0, false},
		"far past":           {400 * dayLen, true},
		"a minute in future": {-time.Minute, false},
		"a year in future":   {-365 * dayLen, false},
	} {
		md := quietMD(recs, ent("gh:a:1", marks.StageNone, c.ago))
		if got := strings.Contains(md, "## Quiet since applied"); got != c.want {
			t.Errorf("%s: listed=%v, want %v\n%s", name, got, c.want, md)
		}
	}
	md := quietMD(recs, ent("gh:a:1", marks.StageInterview, 12*dayLen+3*time.Hour))
	for _, want := range []string{"## Quiet since applied (1)", "**Edge** at Acme: interview, 12 days quiet", "key `gh:a:1`"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q:\n%s", want, md)
		}
	}
}

func TestQuietStagesAndOrder(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", posting.Digested, "Plain", nil), rec("gh:a:2", posting.Digested, "Screen", nil),
		rec("gh:a:3", posting.Digested, "Talk", nil), rec("gh:a:4", posting.Digested, "Offer", nil),
		rec("gh:a:5", posting.Digested, "Rejected", nil), rec("gh:a:6", posting.Digested, "Withdrawn", nil),
		rec("gh:a:7", posting.Digested, "Tie", nil),
	}
	md := quietMD(recs,
		ent("gh:a:1", marks.StageNone, 11*dayLen), ent("gh:a:2", marks.StageScreening, 30*dayLen),
		ent("gh:a:3", marks.StageInterview, 20*dayLen), ent("gh:a:4", marks.StageOffer, 50*dayLen),
		ent("gh:a:5", marks.StageRejected, 50*dayLen), ent("gh:a:6", marks.StageWithdrawn, 50*dayLen),
		ent("gh:a:7", marks.StageNone, 11*dayLen),
	)
	for _, gone := range []string{"Offer", "Rejected", "Withdrawn"} {
		if strings.Contains(md, "**"+gone+"**") {
			t.Errorf("%s is not waiting on an answer but is listed:\n%s", gone, md)
		}
	}
	idx := func(s string) int { return strings.Index(md, "**"+s+"**") }
	if !(idx("Screen") < idx("Talk") && idx("Talk") < idx("Plain") && idx("Plain") < idx("Tie")) || idx("Screen") < 0 {
		t.Errorf("not longest silence first, ties by key:\n%s", md)
	}
	if !strings.Contains(md, "(4)") {
		t.Errorf("count:\n%s", md)
	}
	// A hand-built entry of another mark is not an application.
	other := func() (posting.Key, marks.Entry) {
		return "gh:a:9", marks.Entry{Key: "gh:a:9", Mark: marks.Saved, MarkedAt: quietNow.Add(-90 * dayLen)}
	}
	if md := quietMD(recs, other); strings.Contains(md, "Quiet since applied") {
		t.Errorf("a saved posting is quiet:\n%s", md)
	}
}

func TestQuietCapSaysHowManyMore(t *testing.T) {
	var recs []ledger.Record
	var es []func() (posting.Key, marks.Entry)
	n := MaxQuiet + 7
	for i := range n {
		k := fmt.Sprintf("gh:a:%03d", i)
		recs = append(recs, rec(k, posting.Digested, fmt.Sprintf("Role%03d", i), nil))
		es = append(es, ent(k, marks.StageNone, time.Duration(QuietDays+i)*dayLen))
	}
	md := quietMD(recs, es...)
	if !strings.Contains(md, fmt.Sprintf("## Quiet since applied (%d)", n)) {
		t.Errorf("the heading must carry the full count %d:\n%s", n, md)
	}
	if got := strings.Count(md, "days quiet"); got != MaxQuiet {
		t.Errorf("%d rows listed, want %d", got, MaxQuiet)
	}
	if !strings.Contains(md, "…and 7 more in Jobs") {
		t.Errorf("no \"and K more\" line:\n%s", md)
	}
	if i, j := strings.Index(md, "Role026"), strings.Index(md, "Role025"); i < 0 || j < 0 || i > j {
		t.Errorf("order:\n%s", md)
	}
	// Exactly the cap: no "more" line.
	md = quietMD(recs, es[:MaxQuiet]...)
	if strings.Contains(md, "more in Jobs") || strings.Count(md, "days quiet") != MaxQuiet {
		t.Errorf("at the cap:\n%s", md)
	}
}

func TestQuietEmptyAndUnreadable(t *testing.T) {
	recs := []ledger.Record{rec("gh:a:1", posting.Digested, "Edge", nil)}
	if md := quietMD(recs); strings.Contains(md, "Quiet since applied") {
		t.Errorf("an empty store lists a section:\n%s", md)
	}
	md, _ := RenderWith(recs, nil, quietNow, Counts{}, Options{})
	if strings.Contains(md, "Quiet since applied") {
		t.Errorf("no Quiet option, still a section:\n%s", md)
	}
	md, _ = RenderWith(recs, nil, quietNow, Counts{}, Options{Quiet: &Quiet{Err: errors.New("marks: corrupt store: stages.jsonl line 3")}})
	if !strings.Contains(md, "## Quiet since applied") || !strings.Contains(md, "could not be read") || !strings.Contains(md, "does not mean none have") {
		t.Errorf("an unreadable store must say so:\n%s", md)
	}
	if strings.Contains(md, "days quiet") || strings.Contains(md, "(0)") {
		t.Errorf("an unreadable store rendered as a list:\n%s", md)
	}
}

func TestQuietEscapesBoardText(t *testing.T) {
	r := rec("gh:a:1", posting.Digested, "[x](https://evil.example) <b>hi</b>\n# heading", nil)
	r.Posting.Company = "Acme](javascript:alert(1)) *bold*"
	md := quietMD([]ledger.Record{r}, ent("gh:a:1", marks.StageNone, 30*dayLen))
	for _, bad := range []string{"](https://evil", "](javascript", "<b>", "\n# heading"} {
		if strings.Contains(md, bad) {
			t.Errorf("unescaped %q:\n%s", bad, md)
		}
	}
	// A posting the ledger no longer holds is still listed, by key.
	md = quietMD(nil, ent("gh:gone:1", marks.StageNone, 30*dayLen))
	if !strings.Contains(md, "key `gh:gone:1`") {
		t.Errorf("a posting missing from the ledger dropped out:\n%s", md)
	}
}

// From a real store: a stage change restarts the clock, a note does not, and a
// stage file that cannot be read is said, not skipped.
func TestQuietFromTheMarksStore(t *testing.T) {
	dir := marks.Dir(t.TempDir())
	t0 := quietNow.Add(-40 * dayLen)
	k, k2 := posting.Key("gh:a:1"), posting.Key("gh:a:2")
	for _, key := range []posting.Key{k, k2} {
		if _, err := marks.SetApplied(dir, key, 0, t0); err != nil {
			t.Fatal(err)
		}
	}
	recs := []ledger.Record{rec(string(k), posting.Digested, "One", nil), rec(string(k2), posting.Digested, "Two", nil)}
	render := func() string {
		a, err := marks.ReadApplied(dir)
		md, _ := RenderWith(recs, nil, quietNow, Counts{}, Options{Quiet: &Quiet{Applied: a, Err: err}})
		return md
	}
	if md := render(); !strings.Contains(md, "(2)") || !strings.Contains(md, "40 days quiet") {
		t.Fatalf("both applied 40 days ago:\n%s", md)
	}
	if _, err := marks.SetStage(dir, k, marks.StageInterview, quietNow.Add(-2*dayLen)); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetNote(dir, k2, "called them", quietNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	md := render()
	if strings.Contains(md, "**One**") || !strings.Contains(md, "**Two**") || strings.Contains(md, "called them") {
		t.Fatalf("a stage 2 days ago restarts the clock; a note does not; and no note is shown:\n%s", md)
	}
	if err := os.WriteFile(filepath.Join(dir, "stages.jsonl"), []byte("not json\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	md = render()
	if !strings.Contains(md, "could not be read") || strings.Contains(md, "days quiet") {
		t.Fatalf("corrupt stages must be reported, not rendered as nothing quiet:\n%s", md)
	}
}
