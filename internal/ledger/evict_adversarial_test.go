package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

var bg = context.Background()

// Escapes, line endings and normalization forms all come back byte for byte,
// both in the process that wrote them and after a restart.
func TestAdvEvictBodyForms(t *testing.T) {
	forms := map[string]string{
		"html specials":  "a <b> & c </script>     d",
		"crlf":           "line1\r\nline2\rline3\n",
		"nul and ctrl":   "a\x00b\x01c\x1fd\x7f",
		"nfc":            "café",
		"nfd":            "café",
		"emoji":          "go \U0001F680 \U0001F9D1‍\U0001F4BB",
		"quotes":         "\"q\" \\ / \t",
		"literal escape": `< not an escape \n`,
		"bom":            "\xef\xbb\xbfstart",
	}
	for name, d := range forms {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			f := mustOpen(t, dir)
			appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, d), ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
			if g, _ := f.Get("gh:a:1"); g.Posting.Description != d || g.BodyEvicted {
				t.Fatalf("live process: got %q evicted=%v, want %q", g.Posting.Description, g.BodyEvicted, d)
			}
			f.Close()
			f = mustOpen(t, dir)
			defer f.Close()
			if g, _ := f.Get("gh:a:1"); g.Posting.Description != d || g.BodyEvicted {
				t.Fatalf("after reopen: got %q evicted=%v, want %q", g.Posting.Description, g.BodyEvicted, d)
			}
		})
	}
}

// Another writer appends a newer posting-bearing line for an evicted key after
// Open. This File's record is the folded state it holds; the body it hands out
// must be that record's, never the other writer's, and never one the other
// writer's bytes displaced.
func TestAdvEvictForeignAppend(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	old := body(100, "old-")
	appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, old), bodyEvent("gh:a:2", posting.Live, t0, "other"),
		ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
	foreign, err := os.OpenFile(eventsPath(dir), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	line := `{"v":1,"at":"2026-10-02T00:00:00Z","key":"gh:a:1","status":"live","posting":{"key":"gh:a:1","title":"T","description":"` +
		strings.Repeat("N", 100) + `"}}` + "\n"
	if _, err := foreign.WriteString(line); err != nil {
		t.Fatal(err)
	}
	foreign.Close()
	g, _ := f.Get("gh:a:1")
	if d := g.Posting.Description; d != old && d != "" {
		t.Fatalf("Get returned %q: neither the folded record's body nor empty", d)
	}
	if g.Status != posting.Dead {
		t.Fatalf("status %s", g.Status)
	}
	// This File keeps writing at its own goodSize. Offsets of the existing keys
	// stay valid because nothing before goodSize moved.
	if err := f.Append(bg, ev("gh:a:2", posting.Dead, t0.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if g, _ := f.Get("gh:a:1"); g.Posting.Description != old {
		t.Fatalf("after own append: %q", g.Posting.Description)
	}
	if g, _ := f.Get("gh:a:2"); g.Posting.Description != "other" {
		t.Fatalf("a:2 after own append: %q", g.Posting.Description)
	}
}

// A body replaced by another of the same length, in place, is caught.
func TestAdvEvictSameLengthDifferentBody(t *testing.T) {
	for _, pair := range [][2]string{{"AAAA", "BBBB"}, {"café", "café"[:5]}, {"x<y", `x\u`}} {
		a, b := pair[0], pair[1]
		if len(a) != len(b) {
			continue
		}
		dir := t.TempDir()
		f := mustOpen(t, dir)
		appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, a), ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
		raw, _ := os.ReadFile(eventsPath(dir))
		if !bytes.Contains(raw, []byte(`"description":"`+a)) {
			f.Close()
			continue
		}
		raw = bytes.Replace(raw, []byte(`"description":"`+a), []byte(`"description":"`+b), 1)
		if err := os.WriteFile(eventsPath(dir), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Hydrate("gh:a:1"); !errors.Is(err, ErrBodyUnavailable) {
			t.Errorf("%q->%q: Hydrate err = %v", a, b, err)
		}
		f.Close()
	}
}

func rec1(t *testing.T, f *File, k posting.Key) Record {
	t.Helper()
	r, ok := f.Get(k)
	if !ok {
		t.Fatalf("no %s", k)
	}
	return r
}

// Live -> Dead -> Duplicate (with a canonical pointer) -> Dropped -> reopen,
// then a closed record whose last posting event is older than later
// status-only events.
func TestAdvEvictStatusChains(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	k := posting.Key("gh:a:1")
	A, B := body(300, "A-"), body(300, "B-")
	dupEv := ev("gh:a:1", posting.Duplicate, t0.Add(2*time.Hour))
	dupEv.Annotations = map[string]string{"duplicate_of": "gh:a:9"}
	appendAll(t, f, bodyEvent(k, posting.Live, t0, A), ev("gh:a:1", posting.Dead, t0.Add(time.Hour)), dupEv,
		ev("gh:a:1", posting.Dropped, t0.Add(3*time.Hour)), ev("gh:a:1", posting.Dead, t0.Add(4*time.Hour)))
	for i := 0; i < 3; i++ { // repeated status-only closes
		appendAll(t, f, ev("gh:a:1", posting.Dropped, t0.Add(time.Duration(5+i)*time.Hour)))
	}
	if r := rec1(t, f, k); r.Posting.Description != A || r.Annotations["duplicate_of"] != "gh:a:9" || r.Status != posting.Dropped {
		t.Fatalf("after chain: %+v", r)
	}
	appendAll(t, f, ev("gh:a:1", posting.Live, t0.Add(9*time.Hour)))
	if r := f.All()[0]; r.BodyEvicted || r.Posting.Description != A {
		t.Fatalf("after reopen resident: %+v", r)
	}
	// A new body, closed again, then reopened without a posting: B, not A.
	appendAll(t, f, bodyEvent(k, posting.Dead, t0.Add(10*time.Hour), B), ev("gh:a:1", posting.Live, t0.Add(11*time.Hour)))
	if r := f.All()[0]; r.BodyEvicted || r.Posting.Description != B {
		t.Fatalf("after second reopen: %.20q", r.Posting.Description)
	}
	f.Close()
	f = mustOpen(t, dir)
	defer f.Close()
	if r := f.All()[0]; r.BodyEvicted || r.Posting.Description != B {
		t.Fatalf("after restart: %.20q evicted=%v", r.Posting.Description, r.BodyEvicted)
	}
}

// A batch may reopen and close the same key repeatedly. It must fold exactly as
// the same events appended one by one do.
func TestAdvEvictBatchMatchesSequential(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	sts := []posting.Status{posting.Live, posting.Dead, posting.Dropped, posting.Duplicate, posting.Triaged}
	for round := 0; round < 200; round++ {
		var evs []Event
		for i := 0; i < 12; i++ {
			k := posting.Key(fmt.Sprintf("gh:a:%d", rng.Intn(3)))
			at := t0.Add(time.Duration(i) * time.Minute)
			st := sts[rng.Intn(len(sts))]
			if rng.Intn(3) == 0 {
				evs = append(evs, bodyEvent(k, st, at, body(rng.Intn(50), fmt.Sprintf("r%d-%d-", round, i))))
			} else {
				evs = append(evs, ev(string(k), st, at))
			}
		}
		// Seed so keys exist and are closed.
		seed := []Event{bodyEvent("gh:a:0", posting.Dead, t0.Add(-time.Hour), "seed0"), bodyEvent("gh:a:1", posting.Dropped, t0.Add(-time.Hour), "seed1"),
			bodyEvent("gh:a:2", posting.Duplicate, t0.Add(-time.Hour), "seed2")}
		d1, d2 := t.TempDir(), t.TempDir()
		fb, fs := mustOpen(t, d1), mustOpen(t, d2)
		appendAll(t, fb, seed...)
		appendAll(t, fs, seed...)
		errB := fb.AppendBatch(bg, evs)
		var errS error
		for _, e := range evs {
			if errS = fs.Append(bg, e); errS != nil {
				break
			}
		}
		if (errB == nil) != (errS == nil) {
			t.Fatalf("round %d: batch err %v, sequential err %v", round, errB, errS)
		}
		if errB == nil {
			ab, as := fb.All(), fs.All()
			for i := range ab {
				ab[i].LastEvent, as[i].LastEvent = time.Time{}, time.Time{}
				if ab[i].Status != as[i].Status || ab[i].BodyEvicted != as[i].BodyEvicted || ab[i].Posting.Description != as[i].Posting.Description {
					t.Fatalf("round %d key %s: batch %+v seq %+v", round, ab[i].Key, ab[i], as[i])
				}
				gb, gs := rec1(t, fb, ab[i].Key), rec1(t, fs, ab[i].Key)
				if gb.Posting.Description != gs.Posting.Description {
					t.Fatalf("round %d Get %s: batch %q seq %q", round, ab[i].Key, gb.Posting.Description, gs.Posting.Description)
				}
			}
			fb.Close()
			fb = mustOpen(t, d1)
			for _, r := range fb.All() {
				if g := rec1(t, fb, r.Key); g.Posting.Description != rec1(t, fs, r.Key).Posting.Description {
					t.Fatalf("round %d: after restart %s %q vs %q", round, r.Key, g.Posting.Description, rec1(t, fs, r.Key).Posting.Description)
				}
			}
		}
		fb.Close()
		fs.Close()
	}
}

// A reopen whose body cannot be read writes not a byte, even when it rides in a
// batch behind an event for another key.
func TestAdvEvictRefusedReopenWritesNothing(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, "real-body"), bodyEvent("gh:a:2", posting.Live, t0, "two"),
		ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
	raw, _ := os.ReadFile(eventsPath(dir))
	tampered := bytes.Replace(raw, []byte("real-body"), []byte("fake-body"), 1)
	if err := os.WriteFile(eventsPath(dir), tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.AppendBatch(bg, []Event{ev("gh:a:2", posting.Dead, t0.Add(2*time.Hour)), ev("gh:a:1", posting.Live, t0.Add(2*time.Hour))})
	if !errors.Is(err, ErrBodyUnavailable) {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(eventsPath(dir))
	if !bytes.Equal(after, tampered) {
		t.Fatal("a refused batch changed the file")
	}
	if r := rec1(t, f, "gh:a:2"); r.Status != posting.Live {
		t.Fatalf("a:2 folded from a refused batch: %s", r.Status)
	}
	// A later append goes to the same place (goodSize did not move).
	if err := f.Append(bg, ev("gh:a:2", posting.Dead, t0.Add(3*time.Hour))); err != nil {
		t.Fatal(err)
	}
}

// Bodies of every size class around the pack boundaries survive packing and GC.
func TestAdvPackedBodiesSurviveGC(t *testing.T) {
	sizes := []int{1, 2, packMax - 1, packMax, packMax + 1, packChunk - 1, packChunk, packChunk + 1, 100, 7}
	dir := t.TempDir()
	f := mustOpen(t, dir)
	want := map[posting.Key]string{}
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 300; i++ {
		k := posting.Key(fmt.Sprintf("gh:a:%03d", i))
		n := sizes[rng.Intn(len(sizes))]
		d := fmt.Sprintf("%03d-", i) + strings.Repeat(string(rune('a'+i%26)), n)
		if n > 1 && n < 60000 && i%3 == 0 {
			d = fmt.Sprintf("%03d-", i) + strings.Repeat("é", n/2)
		}
		want[k] = d
		appendAll(t, f, bodyEvent(k, posting.Live, t0, d))
	}
	f.Close()
	f = mustOpen(t, dir)
	defer f.Close()
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	for _, r := range f.All() {
		if r.Posting.Description != want[r.Key] {
			t.Fatalf("%s: got %d bytes, want %d", r.Key, len(r.Posting.Description), len(want[r.Key]))
		}
	}
	// Mutating one record through the API cannot reach another.
	appendAll(t, f, bodyEvent("gh:a:000", posting.Live, t0.Add(time.Hour), "changed"))
	runtime.GC()
	for _, r := range f.All() {
		w := want[r.Key]
		if r.Key == "gh:a:000" {
			w = "changed"
		}
		if r.Posting.Description != w {
			t.Fatalf("after mutate %s differs", r.Key)
		}
	}
}

// Hydrate, Get, All, AppendBatch (reopening and closing keys), Close and a
// reopen race. Every non-empty body is the one written for that key.
func TestAdvEvictConcurrentLifecycle(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	const keys = 8
	bodies := make([]string, keys)
	for i := range bodies {
		bodies[i] = body(2000+i, fmt.Sprintf("k%d-", i))
		appendAll(t, f, bodyEvent(posting.Key(fmt.Sprintf("gh:a:%d", i)), posting.Dead, t0, bodies[i]))
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var bad error
	var mu sync.Mutex
	fail := func(e error) { mu.Lock(); bad = e; mu.Unlock() }
	check := func(r Record) {
		var i int
		fmt.Sscanf(string(r.Key), "gh:a:%d", &i)
		if r.Posting.Description != "" && r.Posting.Description != bodies[i] {
			fail(fmt.Errorf("%s: wrong body %.12q", r.Key, r.Posting.Description))
		}
		if r.Posting.Description == "" && !r.BodyEvicted {
			fail(fmt.Errorf("%s: empty body not marked evicted", r.Key))
		}
	}
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				k := posting.Key(fmt.Sprintf("gh:a:%d", (n+g)%keys))
				if r, err := f.Hydrate(k); err == nil {
					check(r)
				} else if !errors.Is(err, ErrClosed) {
					fail(err)
				}
				if r, ok := f.Get(k); ok {
					check(r)
				}
				for _, r := range f.All() {
					check(r)
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 60; n++ {
			k := fmt.Sprintf("gh:a:%d", n%keys)
			err := f.AppendBatch(bg, []Event{ev(k, posting.Live, t0.Add(time.Duration(n)*time.Minute)), ev(k, posting.Dead, t0.Add(time.Duration(n)*time.Minute)),
				ev(k, posting.Live, t0.Add(time.Duration(n)*time.Minute))})
			if err != nil && !errors.Is(err, ErrClosed) {
				fail(err)
			}
			if err := f.Append(bg, ev(k, posting.Dropped, t0.Add(time.Hour))); err != nil && !errors.Is(err, ErrClosed) {
				fail(err)
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	f.Close()
	time.Sleep(10 * time.Millisecond)
	close(stop)
	wg.Wait()
	if bad != nil {
		t.Fatal(bad)
	}
	g := mustOpen(t, dir) // the file the race left replays and still holds every body
	defer g.Close()
	for _, r := range g.All() {
		check(rec1(t, g, r.Key))
	}
	if bad != nil {
		t.Fatal(bad)
	}
}

// A body whose line lies past 2 GiB reads back: offsets are int64.
func TestAdvEvictOffsetPast2GiB(t *testing.T) {
	path := t.TempDir() + "/sparse"
	fh, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	off := int64(3) << 30
	if err := fh.Truncate(off); err != nil {
		t.Skip("no sparse files here")
	}
	e := bodyEvent("gh:a:1", posting.Dead, t0, "far away body")
	line := []byte(`{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:a:1","status":"dead","posting":{"key":"gh:a:1","title":"t","description":"far away body"}}` + "\n")
	_ = e
	if _, err := fh.WriteAt(line, off); err != nil {
		t.Fatal(err)
	}
	s := &slot{Record: Record{Key: "gh:a:1"}, ref: bodyRef{off: off, n: int32(len(line)), dlen: int32(len("far away body")), sum: maphash.String(bodySeed, "far away body")}}
	d, err := s.read(fh, off+int64(len(line)))
	if err != nil || d != "far away body" {
		t.Fatalf("read past 2 GiB: %q %v", d, err)
	}
}
