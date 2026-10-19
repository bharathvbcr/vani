package lca

import (
	"fmt"
	"path/filepath"
	"testing"
)

// synthLCA saves an index shaped like a multi-year LCA import: n employers,
// each with three fiscal years, split across two source files.
func synthLCA(tb testing.TB, dir string, n int) string {
	tb.Helper()
	x := NewIndex()
	for s, fys := range [][]int{{2023, 2024}, {2025}} {
		emps := make(map[string]map[int]Counts, n)
		for i := range n {
			byFY := map[int]Counts{}
			for _, fy := range fys {
				byFY[fy] = Counts{H1B: i%97 + 1, H1BWithdrawn: i % 5, H1B1: i % 3, E3: i % 2}
			}
			emps[fmt.Sprintf("EMPLOYER %07d HOLDINGS INC", i)] = byFY
		}
		if err := x.Merge(Partial{Source: SourceMeta{Name: fmt.Sprintf("lca-%d.csv", s), SHA256: fmt.Sprintf("%064d", s)}, Employers: emps}); err != nil {
			tb.Fatal(err)
		}
	}
	path := filepath.Join(dir, "index.json")
	if err := x.Save(path); err != nil {
		tb.Fatal(err)
	}
	return path
}

// BenchmarkLoad is one run's LCA load on a 200k-employer synthetic index.
//
//	go -C <repo> test -run '^$' -bench BenchmarkLoad -benchmem ./internal/lca/
func BenchmarkLoad(b *testing.B) {
	path := synthLCA(b, b.TempDir(), 200_000)
	b.ReportAllocs()
	for b.Loop() {
		x, err := Load(path)
		if err != nil || x.Employers() != 200_000 {
			b.Fatalf("%v", err)
		}
	}
}
