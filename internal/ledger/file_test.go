package ledger

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func ev(key string, st posting.Status, at time.Time) Event {
	return Event{V: 1, At: at, Key: posting.Key(key), Status: st}
}

func mustOpen(t *testing.T, dir string) *File {
	t.Helper()
	f, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return f
}

func TestRoundTripAndFold(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	ctx := context.Background()
	p := posting.Posting{Key: "gh:a:1", Title: "Eng"}

	e1 := ev("gh:a:1", posting.Live, t0)
	e1.Posting = &p
	e1.Annotations = map[string]string{"triage.x": "a", "keep": "k", MissedRunsKey: "1"}
	e2 := ev("gh:a:1", posting.Triaged, t0.Add(time.Hour))
	e2.Annotations = map[string]string{"triage.x": "b", "keep": ""}
	for _, e := range []Event{e1, e2} {
		if err := f.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	r, ok := f.Get("gh:a:1")
	if !ok {
		t.Fatal("missing")
	}
	if r.FirstSeen != t0 || r.LastEvent != t0.Add(time.Hour) || r.Status != posting.Triaged {
		t.Fatalf("bad times/status: %+v", r)
	}
	if r.Posting.Title != "Eng" || r.MissedRuns != 1 {
		t.Fatalf("posting/missed: %+v", r)
	}
	if !reflect.DeepEqual(r.Annotations, map[string]string{"triage.x": "b"}) {
		t.Fatalf("annotations: %v", r.Annotations)
	}
	// Posting seen again resets missed_runs.
	e3 := ev("gh:a:1", posting.Triaged, t0.Add(2*time.Hour))
	e3.Posting = &p
	if err := f.Append(ctx, e3); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.Get("gh:a:1"); r.MissedRuns != 0 {
		t.Fatalf("missed_runs not reset: %d", r.MissedRuns)
	}
}

func TestReopenRebuildsIdenticalState(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		k := fmt.Sprintf("gh:b:%02d", 19-i)
		e := ev(k, posting.Live, t0.Add(time.Duration(i)*time.Second))
		e.Annotations = map[string]string{"n": fmt.Sprint(i), MissedRunsKey: "1"}
		e.Posting = &posting.Posting{Key: posting.Key(k), Company: "c"}
		if err := f.Append(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	want := f.All()
	f.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if g.Recovered {
		t.Fatal("clean file reported recovered")
	}
	if got := g.All(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state differs after reopen")
	}
	for i := 1; i < len(want); i++ {
		if want[i-1].Key >= want[i].Key {
			t.Fatal("All not sorted")
		}
	}
}

func TestFileModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub", "ledger")
	f := mustOpen(t, dir)
	defer f.Close()
	di, _ := os.Stat(dir)
	fi, _ := os.Stat(filepath.Join(dir, "events.jsonl"))
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("modes dir=%v file=%v", di.Mode().Perm(), fi.Mode().Perm())
	}
}

func TestTornLastLineRecovery(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	ctx := context.Background()
	if err := f.Append(ctx, ev("gh:c:1", posting.Live, t0)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	path := filepath.Join(dir, "events.jsonl")
	fh, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:c:2","sta`)
	fh.Close()

	g := mustOpen(t, dir)
	if !g.Recovered {
		t.Fatal("torn tail not reported")
	}
	if _, ok := g.Get("gh:c:2"); ok {
		t.Fatal("torn event was applied")
	}
	if err := g.Append(ctx, ev("gh:c:3", posting.Live, t0.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	g.Close()

	h := mustOpen(t, dir) // would be a mid-file corruption if the tail were not cut
	defer h.Close()
	if h.Recovered || len(h.All()) != 2 {
		t.Fatalf("after recovery: recovered=%v records=%d", h.Recovered, len(h.All()))
	}
}

func TestTornTailWithNewlineButBadJSON(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	f.Append(context.Background(), ev("gh:c:1", posting.Live, t0))
	f.Close()
	fh, _ := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString("{garbage}\n")
	fh.Close()
	g := mustOpen(t, dir)
	defer g.Close()
	if !g.Recovered || len(g.All()) != 1 {
		t.Fatalf("recovered=%v n=%d", g.Recovered, len(g.All()))
	}
}

func TestMidFileCorruptionRefusesOpen(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	f.Append(context.Background(), ev("gh:d:1", posting.Live, t0))
	f.Append(context.Background(), ev("gh:d:2", posting.Live, t0))
	f.Close()
	path := filepath.Join(dir, "events.jsonl")
	b, _ := os.ReadFile(path)
	lines := strings.SplitAfter(string(b), "\n")
	bad := lines[0] + "{not json}\n" + lines[1]
	os.WriteFile(path, []byte(bad), 0o600)
	if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
	// The failed Open must release the lock.
	os.WriteFile(path, b, 0o600)
	g, err := Open(dir)
	if err != nil {
		t.Fatalf("lock leaked by failed Open: %v", err)
	}
	g.Close()
}

func TestOversizedLineRefused(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	defer f.Close()
	e := ev("gh:e:1", posting.Live, t0)
	e.Annotations = map[string]string{"big": strings.Repeat("a", MaxLineBytes)}
	if err := f.Append(context.Background(), e); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("want ErrLineTooLong, got %v", err)
	}
	if len(f.All()) != 0 {
		t.Fatal("refused event was folded")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "events.jsonl")); fi.Size() != 0 {
		t.Fatal("refused event was written")
	}
}

func TestLargeButLegalLineReplays(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	e := ev("gh:e:2", posting.Live, t0)
	e.Annotations = map[string]string{"big": strings.Repeat("a", MaxLineBytes/2)}
	if err := f.Append(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	f.Close()
	g := mustOpen(t, dir) // far larger than a default bufio.Scanner token
	defer g.Close()
	if r, ok := g.Get("gh:e:2"); !ok || len(r.Annotations["big"]) != MaxLineBytes/2 {
		t.Fatal("long line lost on replay")
	}
}

func TestOversizedLineOnDiskRefusesOpen(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(strings.Repeat("a", MaxLineBytes+10)+"\n"), 0o600)
	if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestSecondOpenIsLocked(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	if _, err := Open(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	f.Close()
	g, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen after close: %v", err)
	}
	g.Close()
}

func TestInvalidEventsRefused(t *testing.T) {
	f := mustOpen(t, t.TempDir())
	defer f.Close()
	good := ev("gh:f:1", posting.Live, t0)
	cases := map[string]func(*Event){
		"version":     func(e *Event) { e.V = 2 },
		"empty key":   func(e *Event) { e.Key = "" },
		"unknown ats": func(e *Event) { e.Key = "zz:1" },
		"no colon":    func(e *Event) { e.Key = "gh" },
		"bad status":  func(e *Event) { e.Status = "bogus" },
		"zero time":   func(e *Event) { e.At = time.Time{} },
		"bad missed":  func(e *Event) { e.Annotations = map[string]string{MissedRunsKey: "x"} },
		"neg missed":  func(e *Event) { e.Annotations = map[string]string{MissedRunsKey: "-1"} },
		"posting key mismatch": func(e *Event) {
			e.Posting = &posting.Posting{Key: "gh:f:2", Title: "other"}
		},
	}
	for name, mut := range cases {
		e := good
		mut(&e)
		if err := f.Append(context.Background(), e); !errors.Is(err, ErrInvalidEvent) {
			t.Errorf("%s: want ErrInvalidEvent, got %v", name, err)
		}
	}
	if len(f.All()) != 0 {
		t.Fatal("invalid event folded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Append(ctx, good); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx: %v", err)
	}
	f.Close()
	if err := f.Append(context.Background(), good); !errors.Is(err, ErrClosed) {
		t.Fatalf("append after close: %v", err)
	}
}

func TestGetAndAllReturnCopies(t *testing.T) {
	f := mustOpen(t, t.TempDir())
	defer f.Close()
	e := ev("gh:g:1", posting.Live, t0)
	e.Annotations = map[string]string{"a": "1"}
	f.Append(context.Background(), e)

	r, _ := f.Get("gh:g:1")
	r.Annotations["a"] = "mutated"
	r.Annotations["new"] = "x"
	r.Status = posting.Dead
	all := f.All()
	all[0].Annotations["a"] = "mutated2"
	all[0].MissedRuns = 9

	r2, _ := f.Get("gh:g:1")
	if r2.Annotations["a"] != "1" || len(r2.Annotations) != 1 || r2.Status != posting.Live || r2.MissedRuns != 0 {
		t.Fatalf("internal state mutated: %+v", r2)
	}
}

func TestConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				k := fmt.Sprintf("gh:h:%d", i%5)
				e := ev(k, posting.Live, t0.Add(time.Duration(i)*time.Second))
				e.Annotations = map[string]string{"g": fmt.Sprint(g)}
				if err := f.Append(context.Background(), e); err != nil {
					t.Error(err)
					return
				}
				f.Get(posting.Key(k))
				f.All()
			}
		}()
	}
	wg.Wait()
	want := f.All()
	f.Close()
	h := mustOpen(t, dir)
	defer h.Close()
	if len(want) != 5 || !reflect.DeepEqual(h.All(), want) {
		t.Fatal("replay of concurrently written file differs from live state")
	}
}
