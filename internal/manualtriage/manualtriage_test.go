package manualtriage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
	"github.com/bharathvbcr/Vani/internal/posting"
)

var t0 = time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)

func goodSlots() map[string]string {
	return map[string]string{"role_fit": "strong_fit", "seniority": "mid", "sponsorship": "not_stated", "work_mode": "remote"}
}

func hash(c byte) string { return strings.Repeat(string(c), 64) }

func reading(key string, at time.Time) Reading {
	return Reading{Key: posting.Key(key), Slots: goodSlots(), Model: "claude-sonnet-5-5", At: at, Text: hash('a'), Prompt: hash('b')}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func file(dir string) string { return filepath.Join(dir, fileName) }

func TestSetReadLastWinsAndClear(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	if m, err := Read(dir); err != nil || len(m) != 0 {
		t.Fatalf("never written: %v, %v", m, err)
	}
	must(t, Set(dir, reading("gh:acme:1", t0)))
	second := reading("gh:acme:1", t0.Add(time.Minute))
	second.Slots["role_fit"] = "adjacent"
	second.Text = hash('c')
	must(t, Set(dir, second))
	must(t, Set(dir, reading("gh:acme:2", t0)))
	m, err := Read(dir)
	must(t, err)
	if len(m) != 2 {
		t.Fatalf("%d readings, want 2", len(m))
	}
	got := m["gh:acme:1"]
	if got.Slots["role_fit"] != "adjacent" || got.Text != hash('c') || !got.At.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last line did not win: %+v", got)
	}
	had, err := Clear(dir, "gh:acme:1", t0.Add(2*time.Minute))
	if err != nil || !had {
		t.Fatalf("clear: had=%v err=%v", had, err)
	}
	if m, _ := Read(dir); len(m) != 1 || m["gh:acme:2"].Key == "" {
		t.Fatalf("after clear: %v", m)
	}
	// The history stays: three sets and one clear.
	raw, _ := os.ReadFile(file(dir))
	if n := bytes.Count(raw, []byte("\n")); n != 4 {
		t.Fatalf("%d lines kept, want 4", n)
	}
}

func TestClearWithNothingWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	had, err := Clear(dir, "gh:acme:9", t0)
	if err != nil || had {
		t.Fatalf("had=%v err=%v", had, err)
	}
	if st, err := os.Stat(file(dir)); err == nil && st.Size() != 0 {
		t.Fatalf("a clear of nothing wrote %d bytes", st.Size())
	}
	if m, err := Read(dir); err != nil || len(m) != 0 {
		t.Fatalf("%v %v", m, err)
	}
}

func TestInvalidWritesAreRefusedAndWriteNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	cases := map[string]func(*Reading){
		"missing slot":   func(r *Reading) { delete(r.Slots, "work_mode") },
		"unlisted value": func(r *Reading) { r.Slots["role_fit"] = "perfect" },
		"none":           func(r *Reading) { r.Slots["role_fit"] = "none" },
		"extra slot":     func(r *Reading) { r.Slots["category"] = "x" },
		"no slots":       func(r *Reading) { r.Slots = nil },
		"short hash":     func(r *Reading) { r.Text = "abc" },
		"non-hex hash":   func(r *Reading) { r.Prompt = strings.Repeat("z", 64) },
		"empty model":    func(r *Reading) { r.Model = "" },
		"model space":    func(r *Reading) { r.Model = "claude sonnet" },
		"model huge":     func(r *Reading) { r.Model = strings.Repeat("m", MaxModelBytes+1) },
		"zero time":      func(r *Reading) { r.At = time.Time{} },
		"empty key":      func(r *Reading) { r.Key = "" },
		"key no ats":     func(r *Reading) { r.Key = "nocolon" },
		"key control":    func(r *Reading) { r.Key = "gh:a:\x00" },
		"key huge":       func(r *Reading) { r.Key = posting.Key("gh:" + strings.Repeat("k", 600)) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			r := reading("gh:acme:1", t0)
			mut(&r)
			if err := Set(dir, r); !errors.Is(err, ErrInvalid) {
				t.Fatalf("got %v, want ErrInvalid", err)
			}
			if st, err := os.Stat(file(dir)); err == nil && st.Size() != 0 {
				t.Fatalf("an invalid reading wrote %d bytes", st.Size())
			}
		})
	}
}

// The longest line a valid reading can encode to must fit the line bound: every
// byte of a key and model id may cost six bytes escaped.
func TestWorstCaseLineFits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	r := reading("gh:"+strings.Repeat("<", 509), t0)
	r.Model = strings.Repeat("<", MaxModelBytes)
	must(t, Set(dir, r))
	if m, err := Read(dir); err != nil || len(m) != 1 {
		t.Fatalf("worst case did not read back: %v %v", m, err)
	}
}

func TestTornTailIsSkippedAndCutByTheNextWrite(t *testing.T) {
	for name, tail := range map[string]string{
		"half a json line":      `{"v":1,"at":"2026-10-24T10:00:00Z","key":"gh:acme:2","op":"se`,
		"garbage no newline":    `\x00\x00 not json`,
		"whole json no newline": `{"v":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "retriage")
			must(t, Set(dir, reading("gh:acme:1", t0)))
			f, err := os.OpenFile(file(dir), os.O_WRONLY|os.O_APPEND, 0o600)
			must(t, err)
			_, err = f.WriteString(tail)
			must(t, err)
			must(t, f.Close())
			m, err := Read(dir)
			if err != nil || len(m) != 1 {
				t.Fatalf("torn tail: %v, %v", m, err)
			}
			must(t, Set(dir, reading("gh:acme:3", t0)))
			m, err = Read(dir)
			if err != nil || len(m) != 2 {
				t.Fatalf("after the next write: %v, %v", m, err)
			}
			raw, _ := os.ReadFile(file(dir))
			if bytes.Contains(raw, []byte(strings.TrimSpace(tail))) {
				t.Fatalf("the torn tail is still in the file")
			}
		})
	}
}

// A line before the last that does not read is corruption, never "empty", and
// a write refuses to land on it. A last line that is whole JSON but not a valid
// event is not torn either: cutting it could erase a reading a newer writer made.
func TestCorruptStoreFailsClosed(t *testing.T) {
	good, _ := json.Marshal(event{V: Version, At: t0, Key: "gh:acme:1", Op: opSet, Slots: goodSlots(), Model: "m", Text: hash('a'), Prompt: hash('b')})
	for name, content := range map[string]string{
		"bad line then good": "{broken}\n" + string(good) + "\n",
		"unknown op last":    string(good) + "\n" + `{"v":1,"at":"2026-10-24T10:00:00Z","key":"gh:acme:1","op":"merge"}` + "\n",
		"newer version last": string(good) + "\n" + `{"v":2,"at":"2026-10-24T10:00:00Z","key":"gh:acme:1","op":"set"}` + "\n",
		"unknown field":      `{"v":1,"at":"2026-10-24T10:00:00Z","key":"gh:acme:1","op":"clear","extra":1}` + "\n",
		"unlisted slot":      strings.Replace(string(good), "strong_fit", "perfect", 1) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "retriage")
			must(t, os.MkdirAll(dir, 0o700))
			must(t, os.WriteFile(file(dir), []byte(content), 0o600))
			if m, err := Read(dir); !errors.Is(err, ErrCorrupt) || m != nil {
				t.Fatalf("Read: %v, %v", m, err)
			}
			if err := Set(dir, reading("gh:acme:2", t0)); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Set: %v", err)
			}
			if _, err := Clear(dir, "gh:acme:1", t0); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Clear: %v", err)
			}
			if raw, _ := os.ReadFile(file(dir)); string(raw) != content {
				t.Fatalf("a refused write changed the file")
			}
		})
	}
}

func TestOversizedLineAndFileAreCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	must(t, os.MkdirAll(dir, 0o700))
	must(t, os.WriteFile(file(dir), []byte(strings.Repeat("x", maxLineBytes+10)+"\n"), 0o600))
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversized line: %v", err)
	}
	must(t, os.WriteFile(file(dir), bytes.Repeat([]byte("\n"), MaxFileBytes+1), 0o600))
	if _, err := Read(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversized file: %v", err)
	}
	if err := Set(dir, reading("gh:acme:1", t0)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("write onto an oversized file: %v", err)
	}
}

func lineOf(t *testing.T, e event) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	must(t, err)
	return append(b, '\n')
}

// Past the byte bound the store compacts to its live readings and the write
// goes through; it reads back the same.
func TestSizeBoundCompactsInsteadOfGrowing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	must(t, os.MkdirAll(dir, 0o700))
	var buf bytes.Buffer
	for i := 0; buf.Len() < MaxFileBytes-200; i++ {
		buf.Write(lineOf(t, event{V: Version, At: t0.Add(time.Duration(i) * time.Second), Key: "gh:acme:1", Op: opSet, Slots: goodSlots(), Model: "claude-sonnet-5-5", Text: hash('a'), Prompt: hash('b')}))
	}
	must(t, os.WriteFile(file(dir), buf.Bytes(), 0o600))
	must(t, Set(dir, reading("gh:acme:2", t0.Add(time.Hour))))
	st, err := os.Stat(file(dir))
	must(t, err)
	if st.Size() > 2*maxLineBytes {
		t.Fatalf("store is %d bytes after compaction", st.Size())
	}
	m, err := Read(dir)
	if err != nil || len(m) != 2 || m["gh:acme:1"].Key == "" {
		t.Fatalf("after compaction: %v %v", m, err)
	}
}

func TestEntryCapRefusesNewKeysButNotUpdates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	must(t, os.MkdirAll(dir, 0o700))
	var buf bytes.Buffer
	for i := 0; i < MaxEntries; i++ {
		buf.Write(lineOf(t, event{V: Version, At: t0, Key: posting.Key(fmt.Sprintf("gh:c:%d", i)), Op: opSet, Slots: goodSlots(), Model: "m", Text: hash('a'), Prompt: hash('b')}))
	}
	if buf.Len() > MaxFileBytes {
		t.Fatalf("the cap fixture is %d bytes, over the file bound", buf.Len())
	}
	must(t, os.WriteFile(file(dir), buf.Bytes(), 0o600))
	if err := Set(dir, reading("gh:new:1", t0)); !errors.Is(err, ErrFull) {
		t.Fatalf("a new key past the cap: %v", err)
	}
	must(t, Set(dir, reading("gh:c:7", t0.Add(time.Minute))))
	if _, err := Clear(dir, "gh:c:7", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	must(t, Set(dir, reading("gh:new:1", t0)))
}

func TestConcurrentWritersLoseNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	const writers, each = 12, 6
	var wg sync.WaitGroup
	errs := make(chan error, 2*writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				r := reading(fmt.Sprintf("gh:w%d:%d", w, i), t0)
				errs <- Set(dir, r)
				errs <- Set(dir, reading("gh:shared:1", t0.Add(time.Duration(w*each+i)*time.Second)))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	m, err := Read(dir)
	must(t, err)
	if len(m) != writers*each+1 {
		t.Fatalf("%d readings, want %d", len(m), writers*each+1)
	}
	if want := t0.Add(time.Duration(writers*each-1) * time.Second); m["gh:shared:1"].At.After(want) {
		t.Fatalf("shared key has a time from the future: %v", m["gh:shared:1"].At)
	}
}

// Another process holding the flock keeps a writer out for lockWait and then
// says so; the reading is not half-written.
func TestWriterGivesUpWhenAnotherHoldsTheLock(t *testing.T) {
	old := lockWait
	lockWait = 150 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	dir := filepath.Join(t.TempDir(), "retriage")
	must(t, os.MkdirAll(dir, 0o700))
	l, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	must(t, err)
	defer l.Close()
	must(t, filelock.TryLock(l, true))
	start := time.Now()
	if err := Set(dir, reading("gh:acme:1", t0)); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
	if time.Since(start) < lockWait {
		t.Fatalf("gave up after %v, before the wait", time.Since(start))
	}
	if _, err := os.Stat(file(dir)); err == nil {
		t.Fatalf("a refused write created the store")
	}
	must(t, filelock.Unlock(l))
	must(t, Set(dir, reading("gh:acme:1", t0)))
}

func TestAskLockAdmitsOneAtATime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	unlock, err := TryAskLock(dir)
	must(t, err)
	if _, err := TryAskLock(dir); !errors.Is(err, ErrBusy) {
		t.Fatalf("second holder: %v", err)
	}
	unlock()
	unlock2, err := TryAskLock(dir)
	must(t, err)
	unlock2()
}

func TestSymlinkedStoreIsRefusedAndTargetUntouched(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	must(t, os.MkdirAll(dir, 0o700))
	target := filepath.Join(t.TempDir(), "elsewhere.jsonl")
	must(t, os.WriteFile(target, nil, 0o600))
	must(t, os.Symlink(target, file(dir)))
	if err := Set(dir, reading("gh:acme:1", t0)); err == nil {
		t.Fatal("a symlinked store took a write")
	}
	if raw, _ := os.ReadFile(target); len(raw) != 0 {
		t.Fatalf("the link target was written: %q", raw)
	}
}

func TestTextHashAndState(t *testing.T) {
	p := posting.Posting{Key: "gh:acme:1", Title: "Engineer", Location: "Austin, TX", Description: "Build inference."}
	h := TextSHA256(p)
	if len(h) != 64 || h != TextSHA256(p) {
		t.Fatalf("hash %q is not stable", h)
	}
	for name, mut := range map[string]func(*posting.Posting){
		"title":       func(p *posting.Posting) { p.Title = "Senior Engineer" },
		"location":    func(p *posting.Posting) { p.Location = "Remote" },
		"description": func(p *posting.Posting) { p.Description += " More." },
	} {
		q := p
		mut(&q)
		if TextSHA256(q) == h {
			t.Errorf("a changed %s kept the hash", name)
		}
	}
	// Fields the question does not carry do not stale a reading.
	q := p
	q.URL, q.Company = "https://x.invalid/1", "Acme"
	if TextSHA256(q) != h {
		t.Error("fields outside the question changed the hash")
	}
	// Text beyond the question's own cut does not either.
	long := p
	long.Description = strings.Repeat("a", 30<<10)
	longer := long
	longer.Description += "tail"
	if TextSHA256(long) != TextSHA256(longer) {
		t.Error("text past the cut changed the hash")
	}

	rd := Reading{Text: h}
	if rd.StateOf(p, false) != Current || rd.StateOf(q, false) != Current {
		t.Error("an unchanged posting is not current")
	}
	changed := p
	changed.Description = "Different."
	if rd.StateOf(changed, false) != TextChanged {
		t.Error("changed text is not reported")
	}
	if rd.StateOf(p, true) != TextGone {
		t.Error("gone text is not reported as gone")
	}
	if PromptSHA256("a", "b") == PromptSHA256("a", "c") || PromptSHA256("ab", "") == PromptSHA256("a", "b") {
		t.Error("prompt hash does not separate its parts")
	}
}
