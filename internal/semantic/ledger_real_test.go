//go:build tessl && darwin && arm64

package semantic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/kbsync"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestTesslRealLedgerRescoring re-scores the owner's real postings with the
// current chunker and score into a throwaway cache and compares them with the
// scores a `vani jobs list --json` dump holds. It reads the ledger without a lock
// and writes only to a temp dir. It prints counts, rank agreement and the biggest
// movers' company and title (public posting text), never bank or config content.
//
//	SEMANTIC_REAL_LEDGER=<ledger dir> SEMANTIC_REAL_CONFIG=<config.json>
//	SEMANTIC_REAL_BASELINE=<jobs list --json dump> KBSYNC_BANK=<bank.json>
//	VANI_TESSL_SNAPSHOT=<snapshot dir> go test -tags tessl ./internal/semantic -run LedgerRescoring -v
func TestTesslRealLedgerRescoring(t *testing.T) {
	dir, cfgPath, basePath := os.Getenv("SEMANTIC_REAL_LEDGER"), os.Getenv("SEMANTIC_REAL_CONFIG"), os.Getenv("SEMANTIC_REAL_BASELINE")
	if dir == "" || cfgPath == "" || basePath == "" {
		t.Skip("set SEMANTIC_REAL_LEDGER, SEMANTIC_REAL_CONFIG, SEMANTIC_REAL_BASELINE and KBSYNC_BANK")
	}
	var cfg struct {
		Target string `json:"target_summary"`
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var base struct {
		Jobs []struct {
			Key      string `json:"key"`
			Company  string `json:"company"`
			Title    string `json:"title"`
			Semantic *struct {
				Score float64 `json:"score"`
			} `json:"semantic"`
		} `json:"jobs"`
	}
	if raw, err = os.ReadFile(basePath); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}
	old := map[string]float64{}
	who := map[string]string{}
	for _, j := range base.Jobs {
		if j.Semantic != nil {
			old[j.Key] = j.Semantic.Score
			who[j.Key] = j.Company + ": " + truncRunes(j.Title, 48)
		}
	}
	snap, err := ledger.ReadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]ledger.Record{}
	for _, r := range snap.Records {
		byKey[string(r.Key)] = r
	}
	var docs []Doc
	for k := range old {
		if r, ok := byKey[k]; ok && r.Status != posting.Dead {
			docs = append(docs, Doc{Key: k, Title: r.Posting.Title, Description: r.Posting.Description})
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Key < docs[j].Key })
	if n := os.Getenv("SEMANTIC_REAL_LIMIT"); n != "" {
		var lim int
		fmt.Sscanf(n, "%d", &lim)
		if lim > 0 && lim < len(docs) {
			docs = docs[:lim]
		}
	}

	e := realTessl(t)
	b, err := kbsync.Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, cfg.Target)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMatcher(e, p, filepath.Join(t.TempDir(), "semantic"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	start := time.Now()
	rep, err := m.Warm(ctx, docs, len(docs))
	if err != nil {
		t.Fatalf("warm: %v (%+v)", err, rep)
	}
	t.Logf("re-scored %d postings in %v: %+v", len(docs), time.Since(start).Round(time.Second), rep)

	type row struct {
		key      string
		old, new float64
	}
	var rows []row
	for _, d := range docs {
		mt, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
		if !ok || mt.Thin {
			continue
		}
		rows = append(rows, row{d.Key, old[d.Key], mt.Score})
	}
	n := len(rows)
	var satOld, satNew, ge95Old, ge95New int
	for _, r := range rows {
		if r.old >= 0.999 {
			satOld++
		}
		if r.new >= 0.999 {
			satNew++
		}
		if r.old >= 0.95 {
			ge95Old++
		}
		if r.new >= 0.95 {
			ge95New++
		}
	}
	t.Logf("n=%d  exactly 1.0: %d -> %d   >=0.95: %d -> %d   rank correlation (Spearman) %.3f",
		n, satOld, satNew, ge95Old, ge95New, spearman(rows, func(r row) (float64, float64) { return r.old, r.new }))

	topOld := topKeys(rows, 25, func(r row) float64 { return r.old })
	topNew := topKeys(rows, 25, func(r row) float64 { return r.new })
	shared := 0
	for k := range topNew {
		if topOld[k] {
			shared++
		}
	}
	t.Logf("top 25 by semantic: %d of 25 are the same postings", shared)

	sort.Slice(rows, func(i, j int) bool { return rows[i].new-rows[i].old < rows[j].new-rows[j].old })
	for _, r := range rows[:min(8, n)] {
		t.Logf("  fell  %.2f -> %.2f  %s", r.old, r.new, who[r.key])
	}
	for _, r := range rows[max(0, n-8):] {
		t.Logf("  rose  %.2f -> %.2f  %s", r.old, r.new, who[r.key])
	}
}

func topKeys[T any](rows []T, k int, score func(T) float64) map[string]bool {
	idx := make([]int, len(rows))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return score(rows[idx[a]]) > score(rows[idx[b]]) })
	out := map[string]bool{}
	for _, i := range idx[:min(k, len(idx))] {
		out[fmt.Sprint(i)] = true
	}
	return out
}

func spearman[T any](rows []T, xy func(T) (float64, float64)) float64 {
	n := len(rows)
	if n < 3 {
		return math.NaN()
	}
	rank := func(pick func(T) float64) []float64 {
		idx := make([]int, n)
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return pick(rows[idx[a]]) < pick(rows[idx[b]]) })
		r := make([]float64, n)
		for pos := 0; pos < n; {
			end := pos
			for end+1 < n && pick(rows[idx[end+1]]) == pick(rows[idx[pos]]) {
				end++
			}
			avg := float64(pos+end)/2 + 1
			for q := pos; q <= end; q++ {
				r[idx[q]] = avg
			}
			pos = end + 1
		}
		return r
	}
	rx := rank(func(r T) float64 { x, _ := xy(r); return x })
	ry := rank(func(r T) float64 { _, y := xy(r); return y })
	var sx, sy, sxx, syy, sxy float64
	for i := 0; i < n; i++ {
		sx, sy = sx+rx[i], sy+ry[i]
		sxx, syy, sxy = sxx+rx[i]*rx[i], syy+ry[i]*ry[i], sxy+rx[i]*ry[i]
	}
	fn := float64(n)
	return (fn*sxy - sx*sy) / math.Sqrt((fn*sxx-sx*sx)*(fn*syy-sy*sy))
}
