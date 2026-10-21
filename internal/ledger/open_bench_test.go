package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// benchOpenDir writes a synthetic ledger of n Greenhouse postings straight to
// events.jsonl (appending through File would fsync every line). Four of every
// five records end Dead or Dropped (the mix of a long-lived ledger); the rest
// are Triaged. Each posting carries a 4,000-byte description and three
// annotations. It returns the directory.
func benchOpenDir(tb testing.TB, n int) string {
	tb.Helper()
	dir := tb.TempDir()
	f, err := os.Create(filepath.Join(dir, eventsName))
	if err != nil {
		tb.Fatal(err)
	}
	w := bufio.NewWriter(f)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	desc := fmt.Sprintf("%0*d", 4000, 0)
	for i := range n {
		key, err := posting.GreenhouseKey(fmt.Sprintf("co%03d", i%40), fmt.Sprint(i))
		if err != nil {
			tb.Fatal(err)
		}
		p := posting.Posting{Key: key, ATS: posting.Greenhouse, Company: fmt.Sprintf("Company %03d", i%40),
			Title: fmt.Sprintf("Engineer %d", i), Location: "Remote", URL: "https://example.test/" + string(key), Description: desc, SeenAt: at}
		status := posting.Triaged
		switch i % 5 {
		case 0, 1, 2:
			status = posting.Dead
		case 3:
			status = posting.Dropped
		}
		e := Event{V: SchemaVersion, At: at, Key: key, Status: status, Posting: &p,
			Annotations: map[string]string{"triage.fit": "yes", "score.total": "70", "triage.role_fit": "partial_fit"}}
		line, err := json.Marshal(e)
		if err != nil {
			tb.Fatal(err)
		}
		w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		tb.Fatal(err)
	}
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
	return dir
}

// BenchmarkOpen20k is Open (lock, then replay of every event) on a synthetic
// 20,000-record ledger, 80% Dead or Dropped. The fixture is built outside the
// timer. heap-inuse-delta-B is one extra untimed Open after the loop: the
// runtime.MemStats HeapInuse after a GC with the ledger open, minus the same
// reading just before it (the ledger's retained heap, not an absolute).
//
//	go -C <repo> test -run '^$' -bench BenchmarkOpen20k -benchmem -count=5 ./internal/ledger/
func BenchmarkOpen20k(b *testing.B) {
	const n = 20_000
	dir := benchOpenDir(b, n)
	b.ReportAllocs()
	for b.Loop() {
		f, err := Open(dir)
		if err != nil {
			b.Fatal(err)
		}
		if len(f.recs) != n {
			b.Fatalf("replayed %d records, want %d", len(f.recs), n)
		}
		f.Close()
	}
	b.StopTimer()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(int64(after.HeapInuse)-int64(before.HeapInuse)), "heap-inuse-delta-B")
	runtime.KeepAlive(f)
	f.Close()
}
