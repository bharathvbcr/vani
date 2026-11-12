//go:build !windows

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

// cpuTime is the user and system CPU so far of this process (self) and of the
// children it waited for (the rkb a request runs for the knowledge-base
// profile): under the load other work puts on the machine, wall time per op is
// not the cost, CPU is.
func cpuTime(b *testing.B) (self, children time.Duration) {
	var ru, rc syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		b.Fatal(err)
	}
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &rc); err != nil {
		b.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano()), time.Duration(rc.Utime.Nano() + rc.Stime.Nano())
}

// loopCPU runs body as b.Loop does and reports its CPU per op: cpu-ms/op is
// this process's, child-cpu-ms/op its children's.
func loopCPU(b *testing.B, body func()) {
	b.ReportAllocs()
	self0, child0 := cpuTime(b)
	n := 0
	for b.Loop() {
		body()
		n++
	}
	self, child := cpuTime(b)
	b.ReportMetric(float64(self-self0)/float64(n)/1e6, "cpu-ms/op")
	b.ReportMetric(float64(child-child0)/float64(n)/1e6, "child-cpu-ms/op")
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
	// Kept Blends (jobsblends.go): after the first list, a warm request should
	// compute none.
	b.Logf("kept Blends: %d asked for, %d computed (the first list's included)",
		res.blends.lookups.Load(), res.blends.misses.Load())
	// The kept read (jobsreadkeep.go): after the first list or two, a warm
	// request should miss none.
	r := &res.reads
	b.Logf("kept read, hits/misses: facts %d/%d, learn %d/%d, manual readings %d/%d",
		r.factsHits.Load(), r.factsMisses.Load(), r.learnHits.Load(), r.learnMisses.Load(),
		r.manualHits.Load(), r.manualMisses.Load())
}
