//go:build unix

package employer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// synthIndex saves an index shaped like the real one (one large IRS list,
// one small IPEDS list) with n IRS names, and returns its path.
func synthIndex(tb testing.TB, dir string, n int) string {
	tb.Helper()
	irs := make(map[string]attr, n)
	for i := 0; i < n; i++ {
		irs[fmt.Sprintf("ORGANISATION %07d OF THE NORTHERN DISTRICT", i)] = bitNonprofit
	}
	ipeds := map[string]attr{}
	for i := 0; i < n/50; i++ {
		ipeds[fmt.Sprintf("UNIVERSITY %06d", i)] = bitCapExempt | bitNonprofit
	}
	x := NewIndex()
	for _, p := range []Partial{
		{Source: SourceMeta{Name: "eo.csv", Kind: KindIRSBMF, SHA256: "a"}, Names: irs},
		{Source: SourceMeta{Name: "hd.csv", Kind: KindIPEDS, SHA256: "b"}, Names: ipeds},
	} {
		if err := x.Merge(p); err != nil {
			tb.Fatal(err)
		}
	}
	path := filepath.Join(dir, "index.json")
	if err := x.Save(path); err != nil {
		tb.Fatal(err)
	}
	return path
}

// loadOnce is what one run pays: load, then look a name up. It returns what
// was loaded so a caller can measure the heap it holds.
func loadOnce(mode, path string) (any, Badge, error) {
	switch mode {
	case "full":
		x, err := Load(path)
		if err != nil {
			return nil, Badge{}, err
		}
		return x, x.Lookup("University 000001"), nil
	case "view":
		v, info, err := LoadView(path)
		if err != nil {
			return nil, Badge{}, err
		}
		if info.Rebuilt {
			return nil, Badge{}, fmt.Errorf("view rebuilt (%v): the benchmark would time the fallback", info.Reason)
		}
		return v, v.Lookup("University 000001"), nil
	}
	return nil, Badge{}, fmt.Errorf("unknown mode %q", mode)
}

// BenchmarkLoad compares the full per-source load with the lookup view on a
// 200k-name synthetic index. live-MB is the heap still held after the load.
//
//	go -C <repo> test -run '^$' -bench BenchmarkLoad -benchmem ./internal/employer/
func BenchmarkLoad(b *testing.B) {
	path := synthIndex(b, b.TempDir(), 200_000)
	for _, mode := range []string{"full", "view"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			var live uint64
			for b.Loop() {
				loaded, badge, err := loadOnce(mode, path)
				if err != nil || badge.Confidence != ConfExact {
					b.Fatalf("%+v %v", badge, err)
				}
				b.StopTimer()
				var ms runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&ms)
				live = max(live, ms.HeapAlloc)
				runtime.KeepAlive(loaded)
				b.StartTimer()
			}
			b.ReportMetric(float64(live)/1e6, "live-MB")
		})
	}
}

// TestLoadCostInterleaved runs full and view loads in fresh processes,
// alternating, and reports min-of-N wall time and max RSS for each. Opt in
// with VANI_EMPLOYER_BENCH=N (rounds); VANI_EMPLOYER_BENCH_INDEX names a real
// index.json, otherwise a 1.6M-name synthetic one is generated.
//
//	VANI_EMPLOYER_BENCH=7 go -C <repo> test -run TestLoadCostInterleaved -v ./internal/employer/
func TestLoadCostInterleaved(t *testing.T) {
	rounds, _ := strconv.Atoi(os.Getenv("VANI_EMPLOYER_BENCH"))
	if rounds <= 0 {
		t.Skip("set VANI_EMPLOYER_BENCH=<rounds> to run")
	}
	// A real index is copied first: deriving its view must not write into
	// the state directory it came from.
	path := filepath.Join(t.TempDir(), "index.json")
	if src := os.Getenv("VANI_EMPLOYER_BENCH_INDEX"); src != "" {
		copyFile(t, src, path)
	} else {
		path = synthIndex(t, t.TempDir(), 1_600_000)
	}
	if _, info, err := LoadView(path); err != nil { // derive the view once, outside the timing
		t.Fatal(err)
	} else if info.Rebuilt {
		t.Logf("view derived before timing (%v)", info.Reason)
	}
	wall := map[string][]time.Duration{}
	rss := map[string][]int64{}
	for r := 0; r < rounds; r++ {
		for _, mode := range []string{"full", "view"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoadCostChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), "VANI_EMPLOYER_BENCH_CHILD="+mode, "VANI_EMPLOYER_BENCH_CHILD_INDEX="+path)
			start := time.Now()
			out, err := cmd.CombinedOutput()
			d := time.Since(start)
			if err != nil {
				t.Fatalf("%s child: %v\n%s", mode, err, out)
			}
			ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
			if !ok {
				t.Fatal("no rusage for the child")
			}
			maxrss := int64(ru.Maxrss)
			if runtime.GOOS != "darwin" { // darwin reports bytes, the others KiB
				maxrss *= 1024
			}
			wall[mode] = append(wall[mode], d)
			rss[mode] = append(rss[mode], maxrss)
		}
	}
	for _, mode := range []string{"full", "view"} {
		t.Logf("%-4s min-of-%d: wall %v, max RSS %.1f MB (all wall %v)", mode, rounds,
			slices.Min(wall[mode]).Round(time.Millisecond), float64(slices.Min(rss[mode]))/1e6, wall[mode])
	}
}

// TestLoadCostChild is the process TestLoadCostInterleaved measures.
func TestLoadCostChild(t *testing.T) {
	mode := os.Getenv("VANI_EMPLOYER_BENCH_CHILD")
	if mode == "" {
		t.Skip("run by TestLoadCostInterleaved")
	}
	if _, _, err := loadOnce(mode, os.Getenv("VANI_EMPLOYER_BENCH_CHILD_INDEX")); err != nil {
		t.Fatal(err)
	}
}
