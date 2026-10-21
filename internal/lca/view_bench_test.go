//go:build unix

package lca

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// BenchmarkLoadView is BenchmarkLoad's counterpart: one run's LCA load through
// the view on the same 200k-employer synthetic index, followed by one lookup.
//
//	go -C <repo> test -run '^$' -bench 'BenchmarkLoad' -benchmem ./internal/lca/
func BenchmarkLoadView(b *testing.B) {
	path := synthLCA(b, b.TempDir(), 200_000)
	if _, _, err := LoadView(path); err != nil { // derive the view outside the timing
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		v, info, err := LoadView(path)
		if err != nil || info.Rebuilt || v.Len() != 200_000 || v.Lookup("Employer 0000001 Holdings").Total == 0 {
			b.Fatalf("%+v %v", info, err)
		}
	}
}

// loadOnce is what one pipeline pass pays: load, then one lookup.
func loadOnce(mode, path string) error {
	switch mode {
	case "full":
		x, err := Load(path)
		if err != nil || x.Lookup("Employer 0000001 Holdings").Total == 0 {
			return fmt.Errorf("full load: %v", err)
		}
	case "view":
		v, info, err := LoadView(path)
		if err != nil {
			return err
		}
		if info.Rebuilt {
			return fmt.Errorf("view rebuilt (%v): the benchmark would time the fallback", info.Reason)
		}
		if v.Lookup("Employer 0000001 Holdings").Total == 0 {
			return fmt.Errorf("view lookup missed")
		}
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	return nil
}

// TestLoadCostInterleaved times full and view loads in fresh processes,
// alternating A,B per round, and logs min and range of wall time for each plus
// the machine load (uptime) of every round. Opt in with VANI_LCA_BENCH=N
// (rounds).
//
//	VANI_LCA_BENCH=5 go -C <repo> test -count=1 -run TestLoadCostInterleaved -v ./internal/lca/
func TestLoadCostInterleaved(t *testing.T) {
	rounds, _ := strconv.Atoi(os.Getenv("VANI_LCA_BENCH"))
	if rounds <= 0 {
		t.Skip("set VANI_LCA_BENCH=<rounds> to run")
	}
	path := synthLCA(t, t.TempDir(), 200_000)
	if _, _, err := LoadView(path); err != nil { // derive the view once, outside the timing
		t.Fatal(err)
	}
	wall := map[string][]time.Duration{}
	for r := 0; r < rounds; r++ {
		up, _ := exec.Command("uptime").Output()
		for _, mode := range []string{"full", "view"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoadCostChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), "VANI_LCA_BENCH_CHILD="+mode, "VANI_LCA_BENCH_CHILD_INDEX="+path)
			start := time.Now()
			out, err := cmd.CombinedOutput()
			d := time.Since(start)
			if err != nil {
				t.Fatalf("%s child: %v\n%s", mode, err, out)
			}
			wall[mode] = append(wall[mode], d)
		}
		t.Logf("round %d: full %v, view %v; %s", r+1, wall["full"][r].Round(time.Millisecond), wall["view"][r].Round(time.Millisecond), strings.TrimSpace(string(up)))
	}
	for _, mode := range []string{"full", "view"} {
		t.Logf("%-4s n=%d min %v max %v (200k employers x 3 FY, synthetic; whole child process)", mode, rounds,
			slices.Min(wall[mode]).Round(time.Millisecond), slices.Max(wall[mode]).Round(time.Millisecond))
	}
}

// TestLoadCostChild is the process TestLoadCostInterleaved measures.
func TestLoadCostChild(t *testing.T) {
	mode := os.Getenv("VANI_LCA_BENCH_CHILD")
	if mode == "" {
		t.Skip("run by TestLoadCostInterleaved")
	}
	if err := loadOnce(mode, filepath.Clean(os.Getenv("VANI_LCA_BENCH_CHILD_INDEX"))); err != nil {
		t.Fatal(err)
	}
}
