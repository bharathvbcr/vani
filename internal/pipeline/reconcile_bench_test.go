package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// benchLedger writes a ledger of boards*perBoard Greenhouse postings, each
// with a page-sized description and a few annotations, straight to
// events.jsonl (appending through File would fsync every line), and opens it.
func benchLedger(tb testing.TB, boards, perBoard int) (*ledger.File, []sources.Board, [][]posting.Posting) {
	tb.Helper()
	dir := tb.TempDir()
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		tb.Fatal(err)
	}
	w := bufio.NewWriter(f)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	desc := fmt.Sprintf("%0*d", 4000, 0)
	bs := make([]sources.Board, boards)
	listed := make([][]posting.Posting, boards)
	for b := range boards {
		bs[b] = sources.Board{ATS: posting.Greenhouse, Token: fmt.Sprintf("co%03d", b), Company: fmt.Sprintf("Company %03d", b)}
		for i := range perBoard {
			key, err := posting.GreenhouseKey(bs[b].Token, fmt.Sprint(i))
			if err != nil {
				tb.Fatal(err)
			}
			p := posting.Posting{Key: key, ATS: posting.Greenhouse, Company: bs[b].Company, Title: fmt.Sprintf("Engineer %d", i),
				Location: "Remote", URL: "https://example.test/" + string(key), Description: desc, SeenAt: at}
			e := ledger.Event{V: ledger.SchemaVersion, At: at, Key: key, Status: posting.Triaged, Posting: &p,
				Annotations: map[string]string{DescHashKey: p.DescriptionHash(), "triage.fit": "yes", "score.total": "70"}}
			line, err := json.Marshal(e)
			if err != nil {
				tb.Fatal(err)
			}
			w.Write(append(line, '\n'))
			listed[b] = append(listed[b], p)
		}
	}
	if err := w.Flush(); err != nil {
		tb.Fatal(err)
	}
	f.Close()
	l, err := ledger.Open(dir)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { l.Close() })
	return l, bs, listed
}

// BenchmarkReconcileRun is one run's reconcile of every board against a
// 20k-record ledger, every board listing its postings unchanged (the common
// day: nothing new, nothing gone), so no event is written.
//
//	go -C <repo> test -run '^$' -bench BenchmarkReconcileRun -benchmem ./internal/pipeline/
func BenchmarkReconcileRun(b *testing.B) {
	l, boards, listed := benchLedger(b, 40, 500)
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	b.ReportAllocs()
	for b.Loop() {
		if err := reconcileAll(context.Background(), l, boards, listed, now); err != nil {
			b.Fatal(err)
		}
	}
}
