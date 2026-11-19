package localserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func scriptBinary(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "llama-server")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAttackExitedBeforeReadyVariants(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"exit 0":            {"exit 0", "exit status 0"},
		"exit 0 with noise": {"echo boom >&2; exit 0", "boom"},
		"killed by SIGTERM": {"kill -TERM $$; sleep 30", "signal: terminated"},
		"killed by SIGSEGV": {"kill -SEGV $$; sleep 30", "signal: segmentation fault"},
		"exit 255":          {"exit 255", "exit status 255"},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			_, err := Start(context.Background(), Config{Binary: scriptBinary(t, tc.body), Model: model(t), StartTimeout: time.Minute})
			if err == nil || !strings.Contains(err.Error(), "exited before it was ready") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatalf("took %s", time.Since(start))
			}
		})
	}
}

// A dead child is never taken for ready because something else answers its
// port: waitHealthy checks the exit before it probes.
func TestAttackSquatterOnThePortOfADeadChild(t *testing.T) {
	squat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer squat.Close()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	s := &Server{cmd: cmd, exited: make(chan struct{}), stderr: &capped{limit: 100}}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { s.waitErr = cmd.Wait(); close(s.exited) }()
	<-s.exited
	err := s.waitHealthy(context.Background(), squat.URL+"/health", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exited before it was ready") {
		t.Fatalf("a dead child was reported ready (err %v)", err)
	}
}

// Stop and Exited race waitHealthy and waitErr's write under -race.
func TestAttackStopRacesWaitHealthy(t *testing.T) {
	for i := 0; i < 20; i++ {
		cmd := exec.Command("/bin/sh", "-c", "sleep 30")
		s := &Server{cmd: cmd, exited: make(chan struct{}), stderr: &capped{limit: 100}}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { s.waitErr = cmd.Wait(); close(s.exited) }()
		var wg sync.WaitGroup
		var herr error
		wg.Add(3)
		go func() {
			defer wg.Done()
			herr = s.waitHealthy(context.Background(), "http://127.0.0.1:1/health", 10*time.Second)
		}()
		go func() { defer wg.Done(); time.Sleep(time.Duration(i) * 20 * time.Millisecond); s.Stop() }()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = s.Exited()
				time.Sleep(time.Millisecond)
			}
		}()
		wg.Wait()
		if herr == nil || !strings.Contains(herr.Error(), "exited before it was ready") {
			t.Fatalf("iteration %d: %v", i, herr)
		}
	}
}
