package eval

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// rdDecider answers every slot with its first option and keeps the exact
// context bytes of every question it was sent, by posting key.
type rdDecider struct {
	mu   sync.Mutex
	sent map[string][]byte
}

func (d *rdDecider) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	d.mu.Lock()
	if d.sent == nil {
		d.sent = map[string][]byte{}
	}
	d.sent[q.RecordID] = append([]byte(nil), q.Context...)
	d.mu.Unlock()
	a := decide.Answer{Backend: "llm", Slots: map[string]decide.SlotReading{}}
	for _, s := range q.Slots {
		a.Slots[s.Name] = decide.SlotReading{Reading: decide.Answered, Value: s.Options[0]}
	}
	return a, nil
}

func rdSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// rdRun asks d about every posting with the jd_triage task and returns the
// rows the log holds, by key.
func rdRun(t *testing.T, d decide.Decider, ps []posting.Posting) map[posting.Key]TeacherRow {
	t.Helper()
	path := filepath.Join(t.TempDir(), TeacherFile)
	st, err := OpenTeacherStore(path)
	if err != nil {
		t.Fatal(err)
	}
	run := TeacherRun{
		D: d, Model: "claude-haiku-test", Summary: "ml systems roles", Store: st, Concurrency: 2, LimitNano: 1 << 40,
		Spent: func(context.Context) (int64, error) { return 0, nil },
		Worst: func(decide.Question) int64 { return 1 },
		Now:   func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) },
	}
	var cands []TeacherCandidate
	for _, p := range ps {
		cands = append(cands, TeacherCandidate{Posting: p})
	}
	res, err := run.Run(context.Background(), cands)
	if err != nil || res.Stored != len(ps) {
		t.Fatalf("run = %+v, %v", res, err)
	}
	return rdRows(t, path)
}

func rdRows(t *testing.T, path string) map[posting.Key]TeacherRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[posting.Key]TeacherRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r TeacherRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("row %q: %v", sc.Text(), err)
		}
		out[r.Key] = r
	}
	return out
}

// renderField reads render_sha256 from the row's JSON, so the test compiles
// against code that has no such field and fails there.
func renderField(t *testing.T, r TeacherRow) string {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	s, _ := m["render_sha256"].(string)
	return s
}

func rdPosting(key, title, location, desc string) posting.Posting {
	return posting.Posting{Key: posting.Key(key), Company: "SynthCo", Title: title, Location: location, Description: desc}
}

// The row's render_sha256 is the sha256 of the exact context bytes the
// question carried: what the model read, after rendering and truncation.
func TestTeacherRowRenderSHAIsTheSentContext(t *testing.T) {
	d := &rdDecider{}
	ps := []posting.Posting{
		rdPosting("gh:synth:a", "ML Engineer", "San Francisco, CA", "build inference systems"),
		rdPosting("gh:synth:b", "Data Engineer", "", "pipelines"),
	}
	rows := rdRun(t, d, ps)
	for _, p := range ps {
		sent, ok := d.sent[string(p.Key)]
		if !ok {
			t.Fatalf("%s was not asked", p.Key)
		}
		got := renderField(t, rows[p.Key])
		if got == "" || got != rdSum(sent) {
			t.Errorf("%s: render_sha256 %q, want sha256 of the sent context %s", p.Key, got, rdSum(sent))
		}
	}
}

// A title, a location or a description change each move render_sha256; a
// title or location change leaves text_sha256 (the description hash) alone,
// which is the gap the field closes.
func TestTeacherRowRenderSHAMovesWithTitleLocationAndDescription(t *testing.T) {
	base := rdPosting("gh:synth:base", "ML Engineer", "New York, NY", "serve models")
	title := rdPosting("gh:synth:title", "Staff ML Engineer", "New York, NY", "serve models")
	loc := rdPosting("gh:synth:loc", "ML Engineer", "London, UK", "serve models")
	desc := rdPosting("gh:synth:desc", "ML Engineer", "New York, NY", "serve models at scale")
	rows := rdRun(t, &rdDecider{}, []posting.Posting{base, title, loc, desc})
	b := rows[base.Key]
	if renderField(t, b) == "" {
		t.Fatal("no render_sha256 on the base row")
	}
	for _, p := range []posting.Posting{title, loc, desc} {
		r := rows[p.Key]
		if renderField(t, r) == renderField(t, b) {
			t.Errorf("%s: render_sha256 did not move", p.Key)
		}
	}
	for _, p := range []posting.Posting{title, loc} {
		if rows[p.Key].TextSHA256 != b.TextSHA256 {
			t.Errorf("%s: text_sha256 moved on a title or location change", p.Key)
		}
	}
}

// The hash is taken after truncation: two descriptions that differ only past
// the context cap render the same bytes and so the same hash; one that differs
// inside the cap does not.
func TestTeacherRowRenderSHAIsAfterTruncation(t *testing.T) {
	long := strings.Repeat("x", jdtriage.MaxContextBytes+2048)
	same := rdPosting("gh:synth:one", "Engineer", "Austin, TX", long+"tail one")
	tail := rdPosting("gh:synth:two", "Engineer", "Austin, TX", long+"tail two")
	head := rdPosting("gh:synth:three", "Engineer", "Austin, TX", "y"+long)
	d := &rdDecider{}
	rows := rdRun(t, d, []posting.Posting{same, tail, head})
	if n := len(d.sent[string(same.Key)]); n > jdtriage.MaxContextBytes {
		t.Fatalf("sent context is %d bytes, over the cap", n)
	}
	if renderField(t, rows[same.Key]) != renderField(t, rows[tail.Key]) {
		t.Error("descriptions differing only past the cap rendered different hashes")
	}
	if renderField(t, rows[same.Key]) == renderField(t, rows[head.Key]) {
		t.Error("descriptions differing inside the cap rendered the same hash")
	}
	if rows[same.Key].TextSHA256 == rows[tail.Key].TextSHA256 {
		t.Error("text_sha256 should still see the whole description")
	}
}

// Unicode is hashed as the bytes sent, including a cut at a rune boundary.
func TestTeacherRowRenderSHAUnicode(t *testing.T) {
	// A multi-byte rune straddles the cap so the renderer must back off to a
	// rune start.
	desc := strings.Repeat("é", jdtriage.MaxContextBytes)
	p := rdPosting("gh:synth:uni", "Ingénieur ML — 東京", "Zürich", desc)
	q := rdPosting("gh:synth:uni2", "Ingénieur ML — 東京", "Zurich", desc)
	d := &rdDecider{}
	rows := rdRun(t, d, []posting.Posting{p, q})
	sent := d.sent[string(p.Key)]
	if !utf8.Valid(sent) {
		t.Fatal("sent context is not valid UTF-8")
	}
	if got := renderField(t, rows[p.Key]); got != rdSum(sent) {
		t.Errorf("render_sha256 %q, want %s", got, rdSum(sent))
	}
	if renderField(t, rows[p.Key]) == renderField(t, rows[q.Key]) {
		t.Error("Zürich and Zurich rendered the same hash")
	}
}

// An empty location still gets a hash, and it differs from a set one.
func TestTeacherRowRenderSHAEmptyLocation(t *testing.T) {
	empty := rdPosting("gh:synth:e", "Engineer", "", "work")
	set := rdPosting("gh:synth:s", "Engineer", "Remote", "work")
	rows := rdRun(t, &rdDecider{}, []posting.Posting{empty, set})
	e := renderField(t, rows[empty.Key])
	if len(e) != 64 {
		t.Fatalf("empty location: render_sha256 %q", e)
	}
	if e == renderField(t, rows[set.Key]) {
		t.Error("an empty location rendered the same hash as Remote")
	}
}

// A row written before the field existed decodes, validates and still counts
// as current, so a run does not ask about it again.
func TestTeacherRowWithoutRenderSHAStaysCurrent(t *testing.T) {
	p := rdPosting("gh:synth:old", "Engineer", "Seattle, WA", "old duties")
	old := TeacherRow{
		V: TeacherSchema, Key: p.Key, TextSHA256: HashText(p.Description), Model: "claude-haiku-test",
		FixtureSHA: FixtureSHA(), PromptHash: PromptHash("ml systems roles"),
		Slots: map[string]TeacherSlot{"role_fit": {Reading: string(decide.Answered), Value: "adjacent"}},
		At:    time.Date(2026, 10, 22, 3, 5, 49, 0, time.UTC),
	}
	line, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(line), "render_sha256") {
		t.Fatalf("a row with no render hash wrote the field: %s", line)
	}
	path := filepath.Join(t.TempDir(), TeacherFile)
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenTeacherStore(path)
	if err != nil || st.Len() != 1 || st.Skipped != 0 {
		t.Fatalf("open: len %d skipped %d err %v", st.Len(), st.Skipped, err)
	}
	var meter atomic.Int64
	d := &tkDecider{meter: &meter}
	run := TeacherRun{
		D: d, Model: "claude-haiku-test", Summary: "ml systems roles", Store: st, Concurrency: 1, LimitNano: 1 << 40,
		Spent: func(context.Context) (int64, error) { return 0, nil },
		Worst: func(decide.Question) int64 { return 1 },
		Now:   func() time.Time { return time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC) },
	}
	res, err := run.Run(context.Background(), []TeacherCandidate{{Posting: p}})
	if err != nil || d.calls != 0 || res.Skipped != 1 {
		t.Errorf("an old row was asked again: calls %d result %+v err %v", d.calls, res, err)
	}
}

// The field is additive: the jd task's identity hashes are what live rows
// already carry. Pinned against the values at f254c4d.
func TestTeacherJDIdentityHashesUnchanged(t *testing.T) {
	if got := FixtureSHA(); got != "0cf04ad791d67fb04bf4e1c5be9b3206f61cf356dddf0a3054c9a8b5a13be47d" {
		t.Errorf("FixtureSHA = %s", got)
	}
	if got := PromptHash(""); got != "78447616c361c384028d0135b02feda121e585f05e8ae3e5ae0f4e8d50d9dbf0" {
		t.Errorf("PromptHash(\"\") = %s", got)
	}
	if got := PromptHash("ml systems roles"); got != "06151159b99d90f3fdc06cae1dec4862414efc51a9c540a0b8ef6619db21b05a" {
		t.Errorf("PromptHash(ml systems roles) = %s", got)
	}
}
