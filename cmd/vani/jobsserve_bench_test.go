package main

import (
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// cpuTime is this process's user and system CPU so far: under the load other
// work puts on the machine, wall time per op is not the cost, CPU is.
func cpuTime(b *testing.B) time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		b.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// loopCPU runs body as b.Loop does and reports its CPU per op.
func loopCPU(b *testing.B, body func()) {
	b.ReportAllocs()
	start, n := cpuTime(b), 0
	for b.Loop() {
		body()
		n++
	}
	b.ReportMetric(float64(cpuTime(b)-start)/float64(n)/1e6, "cpu-ms/op")
}

// BenchmarkLiveServe is what `vani jobs serve` costs on a real state dir, by
// the request path itself (runOp and the response write; the pipe adds a copy
// of the reply): its first list (the Reader from the checkpoint and the facts
// store opened, then the list), and a warm request of each kind the Jobs tab
// sends. It reads the state dir as `jobs list` does, and saves the checkpoint
// and the facts store as `jobs list` would. Skipped unless VANI_BENCH_STATE
// names a state dir (VANI_BENCH_CONFIG a config file):
//
//	VANI_NO_KEYCHAIN=1 VANI_BENCH_STATE=<state> VANI_BENCH_CONFIG=<config.json> \
//	  go -C <repo> test -run '^$' -bench BenchmarkLiveServe -benchtime=5x -count=3 ./cmd/vani/
func BenchmarkLiveServe(b *testing.B) {
	state := os.Getenv("VANI_BENCH_STATE")
	if state == "" {
		b.Skip("VANI_BENCH_STATE is not set")
	}
	e := &env{stateDir: state, configPath: os.Getenv("VANI_BENCH_CONFIG"), stdout: io.Discard, stderr: os.Stderr}
	ask := func(b *testing.B, s *jobsServer, args ...string) {
		b.Helper()
		resp := s.runOp("bench", serveOps["list"], args)
		if !resp.OK {
			b.Fatalf("%v: %+v", args, resp.Error)
		}
		s.write(resp)
	}

	b.Run("first_list_new", func(b *testing.B) {
		loopCPU(b, func() {
			res, err := e.openJobsResident()
			if err != nil {
				b.Fatal(err)
			}
			ask(b, newJobsServer(e, res, io.Discard), "--view", "new")
			res.Close()
		})
	})

	res, err := e.openJobsResident()
	if err != nil {
		b.Fatal(err)
	}
	defer res.Close()
	s := newJobsServer(e, res, io.Discard)
	ask(b, s, "--view", "new")
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	b.Logf("heap in use after a list, resident Reader and facts store held: %d MiB", ms.HeapInuse>>20)

	for i, args := range [][]string{
		{"--view", "new"},
		{"--view", "all", "--sort", "company"},
		{"--view", "new", "--sort", "salary"},
		{"--view", "saved"},
		{"--view", "new", "--q", "engineer"},
	} {
		b.Run("warm_list_"+strconv.Itoa(i), func(b *testing.B) {
			loopCPU(b, func() { ask(b, s, args...) })
		})
	}
	b.Run("warm_stats_new", func(b *testing.B) {
		loopCPU(b, func() {
			resp := s.runOp("bench", serveOps["stats"], []string{"--view", "new"})
			if !resp.OK {
				b.Fatal(resp.Error)
			}
			s.write(resp)
		})
	})
}
