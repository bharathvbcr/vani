package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func body(n int, tag string) string { return tag + strings.Repeat("x", n) }

func bodyEvent(key posting.Key, st posting.Status, at time.Time, desc string) Event {
	p := posting.Posting{Key: key, Title: "Title " + string(key), Company: "Co", URL: "https://x.test/" + string(key), Description: desc}
	return Event{V: SchemaVersion, At: at, Key: key, Status: st, Posting: &p}
}

func appendAll(t *testing.T, f *File, evs ...Event) {
	t.Helper()
	for _, e := range evs {
		if err := f.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func eventsPath(dir string) string { return filepath.Join(dir, eventsName) }

// TestEvictClosedKeepsFieldsAndHydrates: a closed record keeps Title, Company
// and URL in All and Get, and its Description comes back on demand.
func TestEvictClosedKeepsFieldsAndHydrates(t *testing.T) {
	for _, st := range []posting.Status{posting.Dead, posting.Dropped, posting.Duplicate} {
		t.Run(string(st), func(t *testing.T) {
			dir := t.TempDir()
			f := mustOpen(t, dir)
			want := body(500, "jd-")
			appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, want), ev("gh:a:1", st, t0.Add(time.Hour)))
			check := func(f *File) {
				t.Helper()
				all := f.All()
				if len(all) != 1 || !all[0].BodyEvicted || all[0].Posting.Description != "" {
					t.Fatalf("All: %+v", all)
				}
				if p := all[0].Posting; p.Title != "Title gh:a:1" || p.Company != "Co" || p.URL != "https://x.test/gh:a:1" {
					t.Fatalf("closed record lost fields: %+v", p)
				}
				g, _ := f.Get("gh:a:1")
				if g.BodyEvicted || g.Posting.Description != want || g.Status != st {
					t.Fatalf("Get: %+v", g)
				}
				h, err := f.Hydrate("gh:a:1")
				if err != nil || h.Posting.Description != want || h.BodyEvicted {
					t.Fatalf("Hydrate: %v %+v", err, h)
				}
				if fl := Full(f, all[0]); fl.Posting.Description != want || fl.BodyEvicted {
					t.Fatalf("Full: %+v", fl)
				}
				// Get must not un-evict the resident record.
				if a := f.All(); !a[0].BodyEvicted || a[0].Posting.Description != "" {
					t.Fatal("Get changed the resident record")
				}
			}
			check(f)
			f.Close()
			f = mustOpen(t, dir)
			defer f.Close()
			check(f) // evicted by replay as well
		})
	}
}

// TestEvictKeepsOpenRecordsWhole: a record outside the three statuses is never evicted.
func TestEvictKeepsOpenRecordsWhole(t *testing.T) {
	f := mustOpen(t, t.TempDir())
	defer f.Close()
	for i, st := range []posting.Status{posting.Discovered, posting.Live, posting.Triaged, posting.Unsure, posting.Scored, posting.Explained, posting.Digested} {
		k := posting.Key(fmt.Sprintf("gh:a:%d", i))
		appendAll(t, f, bodyEvent(k, st, t0, "text"))
	}
	for _, r := range f.All() {
		if r.BodyEvicted || r.Posting.Description != "text" {
			t.Fatalf("%s evicted: %+v", r.Status, r)
		}
	}
}

// TestEvictReopenRestoresDescription: Dead -> Triaged without a new posting
// brings the body back, via Append, AppendBatch and replay.
func TestEvictReopenRestoresDescription(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	want := body(300, "a-")
	appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, want), ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
	appendAll(t, f, ev("gh:a:1", posting.Triaged, t0.Add(2*time.Hour)))
	if r := f.All()[0]; r.BodyEvicted || r.Posting.Description != want {
		t.Fatalf("after Append reopen: %+v", r)
	}
	// Close it again, then reopen through a batch.
	appendAll(t, f, ev("gh:a:1", posting.Dropped, t0.Add(3*time.Hour)))
	if !f.All()[0].BodyEvicted {
		t.Fatal("not evicted again")
	}
	if err := f.AppendBatch(context.Background(), []Event{ev("gh:a:1", posting.Live, t0.Add(4*time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if r := f.All()[0]; r.BodyEvicted || r.Posting.Description != want {
		t.Fatalf("after batch reopen: %+v", r)
	}
	// Live -> Dead -> Live inside one batch, no posting after the first.
	k2 := posting.Key("gh:a:2")
	if err := f.AppendBatch(context.Background(), []Event{
		bodyEvent(k2, posting.Live, t0, "second"), ev(string(k2), posting.Dead, t0.Add(time.Minute)), ev(string(k2), posting.Live, t0.Add(2*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	if r, _ := f.Get(k2); r.Posting.Description != "second" {
		t.Fatalf("in-batch reopen: %+v", r)
	}
	f.Close()
	f = mustOpen(t, dir) // replay path
	defer f.Close()
	for _, r := range f.All() {
		if r.BodyEvicted || r.Posting.Description == "" {
			t.Fatalf("replay left %s evicted: %+v", r.Key, r)
		}
	}
}

// TestEvictBodyInVeryOldEvent: the only body is the first of many events.
func TestEvictBodyInVeryOldEvent(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	want := body(2000, "old-")
	appendAll(t, f, bodyEvent("gh:a:old", posting.Live, t0, want), ev("gh:a:old", posting.Dead, t0.Add(time.Minute)))
	var evs []Event
	for i := range 3000 {
		k := posting.Key("gh:a:n" + strconv.Itoa(i%50))
		evs = append(evs, bodyEvent(k, posting.Triaged, t0.Add(time.Duration(i)*time.Second), body(100, "n")))
		evs = append(evs, ev("gh:a:old", posting.Dead, t0.Add(time.Hour+time.Duration(i)*time.Second)))
	}
	for len(evs) > 0 {
		n := min(len(evs), 1000)
		if err := f.AppendBatch(context.Background(), evs[:n]); err != nil {
			t.Fatal(err)
		}
		evs = evs[n:]
	}
	for range 2 {
		g, _ := f.Get("gh:a:old")
		if g.Posting.Description != want {
			t.Fatalf("old body lost (%d bytes)", len(g.Posting.Description))
		}
		f.Close()
		f = mustOpen(t, dir) // second pass: by replay
	}
	f.Close()
}

// TestEvictFileChangedAfterOpen: a line that is not the one Open saw is an
// error, never another body.
func TestEvictFileChangedAfterOpen(t *testing.T) {
	want := body(200, "real-")
	fixture := func(t *testing.T) (*File, string) {
		dir := t.TempDir()
		f := mustOpen(t, dir)
		appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, want), bodyEvent("gh:a:2", posting.Live, t0, body(200, "real-")),
			ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
		t.Cleanup(func() { f.Close() })
		return f, eventsPath(dir)
	}
	cases := map[string]func(t *testing.T, path string){
		"truncated": func(t *testing.T, p string) {
			if err := os.Truncate(p, 10); err != nil {
				t.Fatal(err)
			}
		},
		"truncated to empty": func(t *testing.T, p string) {
			if err := os.Truncate(p, 0); err != nil {
				t.Fatal(err)
			}
		},
		"same length edit": func(t *testing.T, p string) {
			b, _ := os.ReadFile(p)
			b = bytes.Replace(b, []byte("real-x"), []byte("fake-x"), 1)
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"lines swapped": func(t *testing.T, p string) {
			b, _ := os.ReadFile(p)
			ls := bytes.SplitAfter(b, []byte("\n"))
			ls[0], ls[1] = ls[1], ls[0]
			if err := os.WriteFile(p, bytes.Join(ls, nil), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"shifted by a prefix": func(t *testing.T, p string) {
			b, _ := os.ReadFile(p)
			if err := os.WriteFile(p, append([]byte("{}\n"), b...), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, path := fixture(t)
			mutate(t, path)
			if _, err := f.Hydrate("gh:a:1"); !errors.Is(err, ErrBodyUnavailable) {
				t.Fatalf("Hydrate err = %v, want ErrBodyUnavailable", err)
			}
			g, ok := f.Get("gh:a:1")
			if !ok || g.Posting.Description != "" || !g.BodyEvicted || g.Posting.Title != "Title gh:a:1" {
				t.Fatalf("Get after tamper: %+v", g)
			}
			// A reopen that needs the body is refused and writes nothing.
			err := f.Append(context.Background(), ev("gh:a:1", posting.Triaged, t0.Add(2*time.Hour)))
			if !errors.Is(err, ErrBodyUnavailable) {
				t.Fatalf("reopen err = %v", err)
			}
			if r := f.All()[0]; r.Status != posting.Dead {
				t.Fatalf("refused reopen still folded: %+v", r)
			}
			// An event that needs no body still goes through.
			if err := f.Append(context.Background(), bodyEvent("gh:a:1", posting.Live, t0.Add(3*time.Hour), "fresh")); err != nil {
				t.Fatalf("posting reopen: %v", err)
			}
			if g, _ := f.Get("gh:a:1"); g.Posting.Description != "fresh" || g.BodyEvicted {
				t.Fatalf("after fresh posting: %+v", g)
			}
		})
	}
}

// TestEvictTornTail: a torn last line does not disturb offsets before it.
func TestEvictTornTail(t *testing.T) {
	dir := t.TempDir()
	f := mustOpen(t, dir)
	want := body(100, "t-")
	appendAll(t, f, bodyEvent("gh:a:1", posting.Live, t0, want), ev("gh:a:1", posting.Dead, t0.Add(time.Hour)))
	f.Close()
	fh, err := os.OpenFile(eventsPath(dir), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	fh.WriteString(`{"v":1,"at":"2026-10-01T12:00:00Z","key":"gh:a:9","stat`)
	fh.Close()
	f = mustOpen(t, dir)
	defer f.Close()
	if !f.Recovered {
		t.Fatal("torn tail not seen")
	}
	if g, _ := f.Get("gh:a:1"); g.Posting.Description != want {
		t.Fatalf("hydrate beside torn tail: %+v", g)
	}
	appendAll(t, f, ev("gh:a:1", posting.Triaged, t0.Add(2*time.Hour))) // cuts the tail, reopens
	if g, _ := f.Get("gh:a:1"); g.Posting.Description != want || g.BodyEvicted {
		t.Fatalf("after tail cut: %+v", g)
	}
}

// TestEvictConcurrentAppendDuringHydrate runs readers against writers; run with -race.
func TestEvictConcurrentAppendDuringHydrate(t *testing.T) {
	f := mustOpen(t, t.TempDir())
	defer f.Close()
	const keys = 20
	for i := range keys {
		k := posting.Key("gh:a:" + strconv.Itoa(i))
		appendAll(t, f, bodyEvent(k, posting.Live, t0, body(1000, strconv.Itoa(i)+"-")), ev(string(k), posting.Dead, t0.Add(time.Minute)))
	}
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	for r := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 300 {
				k := posting.Key("gh:a:" + strconv.Itoa((i+r)%keys))
				g, ok := f.Get(k)
				if !ok || !strings.HasPrefix(g.Posting.Description, strconv.Itoa((i+r)%keys)+"-") {
					select {
					case errc <- fmt.Errorf("%s: bad body %q", k, g.Posting.Description[:min(10, len(g.Posting.Description))]):
					default:
					}
					return
				}
				_ = f.All()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 200 {
			k := posting.Key("gh:a:" + strconv.Itoa(i%keys))
			st := []posting.Status{posting.Triaged, posting.Dropped, posting.Dead}[i%3]
			if err := f.Append(context.Background(), ev(string(k), st, t0.Add(time.Duration(i+2)*time.Minute))); err != nil {
				select {
				case errc <- err:
				default:
				}
				return
			}
		}
	}()
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Fatal(err)
	}
}

// refFold is the fold as it was before eviction existed: the oracle.
func refFold(recs map[posting.Key]*Record, e Event) {
	r, ok := recs[e.Key]
	if !ok {
		r = &Record{Key: e.Key, Annotations: map[string]string{}, FirstSeen: e.At}
		recs[e.Key] = r
	}
	r.LastEvent = e.At
	r.Status = e.Status
	if e.Posting != nil {
		r.Posting = *e.Posting
		r.MissedRuns = 0
	}
	for k, v := range e.Annotations {
		if k == MissedRunsKey {
			r.MissedRuns, _ = strconv.Atoi(v)
			continue
		}
		if v == "" {
			delete(r.Annotations, k)
			continue
		}
		r.Annotations[k] = v
	}
}

var allStatuses = []posting.Status{posting.Discovered, posting.Live, posting.Triaged, posting.Unsure, posting.Scored,
	posting.Explained, posting.Digested, posting.Dead, posting.Duplicate, posting.Dropped}

func randomStream(rng *rand.Rand) []Event {
	nkeys := 1 + rng.Intn(6)
	n := 1 + rng.Intn(40)
	descs := []string{"", "short", body(50, "m-"), body(3000, "big-"), "same body", body(40000, "huge-")}
	evs := make([]Event, 0, n)
	for i := range n {
		k := posting.Key("gh:a:" + strconv.Itoa(rng.Intn(nkeys)))
		e := Event{V: SchemaVersion, At: t0.Add(time.Duration(i) * time.Minute), Key: k, Status: allStatuses[rng.Intn(len(allStatuses))]}
		if rng.Intn(3) == 0 || i < nkeys { // some events carry no posting
			p := posting.Posting{Key: k, Title: "T" + strconv.Itoa(rng.Intn(3)), Company: "C", Description: descs[rng.Intn(len(descs))]}
			e.Posting = &p
		}
		if rng.Intn(2) == 0 {
			e.Annotations = map[string]string{"a" + strconv.Itoa(rng.Intn(3)): []string{"", "v1", "v2"}[rng.Intn(3)]}
			if rng.Intn(6) == 0 {
				e.Annotations[MissedRunsKey] = strconv.Itoa(rng.Intn(4))
			}
		}
		evs = append(evs, e)
	}
	return evs
}

// TestEvictReplayEqualsReferenceFold: over 600 random streams (fixed seed:
// every status incl. reopen, duplicate keys, missing descriptions), the File
// built by replay, the File built by Append, and the File built by AppendBatch
// each hold records that, fully rehydrated, equal the pre-eviction fold; All
// differs from it only by dropping the evicted statuses' descriptions.
func TestEvictReplayEqualsReferenceFold(t *testing.T) {
	rng := rand.New(rand.NewSource(20261021))
	for s := range 600 {
		evs := randomStream(rng)
		want := map[posting.Key]*Record{}
		for _, e := range evs {
			refFold(want, e)
		}
		var buf bytes.Buffer
		for _, e := range evs {
			line, _ := json.Marshal(e)
			buf.Write(append(line, '\n'))
		}
		viaReplay := t.TempDir()
		if err := os.WriteFile(eventsPath(viaReplay), buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		viaAppend, viaBatch := t.TempDir(), t.TempDir()
		fa, fb := mustOpen(t, viaAppend), mustOpen(t, viaBatch)
		// Append fsyncs every event, so only one stream in twelve goes through it
		// (every stream still goes through replay and a random-size AppendBatch).
		single := s%12 == 0
		for _, e := range evs {
			if !single {
				break
			}
			if err := fa.Append(context.Background(), e); err != nil {
				t.Fatalf("stream %d: %v", s, err)
			}
		}
		for rest := evs; len(rest) > 0; {
			n := 1 + rng.Intn(len(rest))
			if err := fb.AppendBatch(context.Background(), rest[:n]); err != nil {
				t.Fatalf("stream %d: %v", s, err)
			}
			rest = rest[n:]
		}
		fr := mustOpen(t, viaReplay)
		verify := func(files map[string]*File) {
			for name, f := range files {
				if f.Recovered {
					t.Fatalf("stream %d %s: torn", s, name)
				}
				if len(f.All()) != len(want) {
					t.Fatalf("stream %d %s: %d records, want %d", s, name, len(f.All()), len(want))
				}
				for _, r := range f.All() {
					w := *want[r.Key]
					if evicts(w.Status) && w.Posting.Description != "" {
						if !r.BodyEvicted || r.Posting.Description != "" {
							t.Fatalf("stream %d %s %s: closed body resident: %+v", s, name, r.Key, r)
						}
						w.Posting.Description, w.BodyEvicted = "", true
					}
					if !reflect.DeepEqual(r, w) {
						t.Fatalf("stream %d %s All %s:\n got %+v\nwant %+v", s, name, r.Key, r, w)
					}
				}
				for k, w := range want {
					g, ok := f.Get(k)
					if !ok || !reflect.DeepEqual(g, *w) {
						t.Fatalf("stream %d %s Get %s:\n got %+v\nwant %+v", s, name, k, g, *w)
					}
				}
				f.Close()
			}
		}
		first := map[string]*File{"replay": fr, "batch": fb}
		if single {
			first["append"] = fa
		} else {
			fa.Close()
		}
		verify(first)
		// Also reopen what Append and AppendBatch wrote.
		second := map[string]*File{"batch+reopen": mustOpen(t, viaBatch)}
		if single {
			second["append+reopen"] = mustOpen(t, viaAppend)
		}
		verify(second)
		snap, err := ReadSnapshot(viaReplay)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range snap.Records {
			if !reflect.DeepEqual(r, *want[r.Key]) {
				t.Fatalf("stream %d snapshot %s differs", s, r.Key)
			}
		}
	}
}

// TestEvictMemoryDrops: the bodies of closed records are not retained after Open.
func TestEvictMemoryDrops(t *testing.T) {
	dir := benchOpenDir(t, 2000)
	f := mustOpen(t, dir)
	defer f.Close()
	resident, closed := 0, 0
	for _, r := range f.All() {
		resident += len(r.Posting.Description)
		if evicts(r.Status) {
			closed++
			if !r.BodyEvicted {
				t.Fatalf("%s closed but resident", r.Key)
			}
		}
	}
	if closed != 1600 || resident != 400*4000 {
		t.Fatalf("closed %d, resident bytes %d", closed, resident)
	}
}
