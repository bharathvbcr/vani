package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/jobfacts"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// TestLiveJobsParityDump writes what `jobs list`, `stats`, `gaps` and `show`
// answer over a real state dir, at a pinned clock, to the file
// VANI_PARITY_OUT names. Run it before and after a change to the read path and
// compare the two files byte for byte: equal files are the parity gate for a
// performance change that must not change any reply. It only reads the state
// dir (the lock-free snapshot every jobs read uses); point it at a copy so a
// running daemon cannot change the ledger between the two runs.
//
// Skipped unless VANI_PARITY_STATE and VANI_PARITY_OUT are set:
//
//	VANI_NO_KEYCHAIN=1 VANI_PARITY_STATE=<state copy> VANI_PARITY_CONFIG=<config.json> \
//	  VANI_PARITY_OUT=<file> go -C <repo> test -run '^TestLiveJobsParityDump$' -timeout 60m ./cmd/vani/
func TestLiveJobsParityDump(t *testing.T) {
	state, out := os.Getenv("VANI_PARITY_STATE"), os.Getenv("VANI_PARITY_OUT")
	if state == "" || out == "" {
		t.Skip("VANI_PARITY_STATE and VANI_PARITY_OUT are not set")
	}
	pinned := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return pinned }
	t.Cleanup(func() { now = old })

	var dump bytes.Buffer
	run := func(name string, fn func(*env, []string) error, args ...string) []byte {
		t.Helper()
		var stdout, stderr bytes.Buffer
		e := &env{stateDir: state, configPath: os.Getenv("VANI_PARITY_CONFIG"), stdout: &stdout, stderr: &stderr}
		start := time.Now()
		err := fn(e, args)
		fmt.Fprintf(&dump, "### %s %s (err=%v)\n", name, strings.Join(args, " "), err)
		dump.Write(stdout.Bytes())
		dump.WriteByte('\n')
		t.Logf("%s %s: %d bytes in %s", name, strings.Join(args, " "), stdout.Len(), time.Since(start).Round(time.Millisecond))
		if f := e.lastFacts; f != nil {
			t.Logf("  facts: served %d, derived %d, store %q, %s", f.Served, f.Derived, f.Store, f.Saved)
		}
		return stdout.Bytes()
	}
	// VANI_PARITY_SERVE=1 asks every case of one resident `jobs serve` instead,
	// twice over, and dumps the second (warm) pass, which must equal the first:
	// the dump then compares with a command run's byte for byte (the command
	// prints its reply and a newline; serve's result is the reply).
	passes := 1
	if os.Getenv("VANI_PARITY_SERVE") != "" {
		passes = 2
		e := &env{stateDir: state, configPath: os.Getenv("VANI_PARITY_CONFIG"), stdout: io.Discard, stderr: os.Stderr}
		res, err := e.openJobsResident()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Close() })
		s := newJobsServer(e, res, io.Discard)
		firstPass := map[string]string{}
		run = func(name string, fn func(*env, []string) error, args ...string) []byte {
			t.Helper()
			id := name + " " + strings.Join(args, " ")
			start := time.Now()
			resp := s.runOp(id, fn, slices.DeleteFunc(slices.Clone(args), isJSONFlag))
			var reply []byte
			var rerr error
			if resp.OK {
				reply = append(resp.Result, '\n')
			} else {
				rerr = fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
			}
			t.Logf("serve %s: %d bytes in %s", id, len(reply), time.Since(start).Round(time.Millisecond))
			if prev, ok := firstPass[id]; !ok {
				firstPass[id] = string(reply)
				return reply
			} else if prev != string(reply) {
				t.Errorf("serve %s: the warm answer differs from the first: %s", id, firstDiff(string(reply), prev))
			}
			fmt.Fprintf(&dump, "### %s %s (err=%v)\n", name, strings.Join(args, " "), rerr)
			dump.Write(reply)
			dump.WriteByte('\n')
			return reply
		}
	}
	// How the jobs reads find the ledger checkpoint, before and after: the
	// commands open their Reader with these same options. Logged, never dumped,
	// so a cold and a warm run dump alike and the log says which one ran.
	readerInfo := func(when string) {
		e := &env{stateDir: state}
		rd, err := ledger.OpenReader(filepath.Join(state, "ledger"), e.jobsReaderOptions())
		if err != nil {
			t.Fatalf("reader %s: %v", when, err)
		}
		t.Logf("reader %s: %+v", when, rd.Info())
		rd.Close()
	}
	// VANI_PARITY_STALE_FACTS=1 forgets every third posting's stored facts
	// first, so the run derives those again from text and serves the rest: a
	// partly stale store must dump as a full or an empty one does.
	if os.Getenv("VANI_PARITY_STALE_FACTS") != "" {
		v, err := jobinsight.Default()
		if err != nil {
			t.Fatal(err)
		}
		e := &env{stateDir: state}
		s := jobfacts.Open(e.jobsFactsPath(), jobfacts.Version(v))
		before, i := s.Len(), 0
		s.Prune(func(posting.Key) bool { i++; return i%3 != 0 })
		if err := s.Save(); err != nil {
			t.Fatal(err)
		}
		t.Logf("stale facts: kept %d of %d (%s)", s.Len(), before, s.Info())
	}
	readerInfo("before")
	t.Cleanup(func() { readerInfo("after") })
	list := (*env).jobsList
	cases := [][]string{
		{"--view", "new"},
		{"--view", "new", "--sort", "priority"},
		{"--view", "new", "--sort", "relevance"},
		{"--view", "new", "--sort", "fresh"},
		{"--view", "new", "--sort", "company"},
		{"--view", "new", "--sort", "salary"},
		{"--view", "all"},
		{"--view", "all", "--sort", "company"},
		{"--view", "saved"},
		{"--view", "applied"},
		{"--view", "hidden"},
		{"--view", "pending"},
		{"--view", "new", "--q", "engineer"},
		{"--view", "new", "--remote"},
		{"--view", "new", "--country", "US", "--or-remote"},
		{"--view", "new", "--max-age", "14", "--open"},
		{"--view", "new", "--fit", "strong_fit,partial_fit"},
		{"--view", "all", "--half-life", "7"},
	}
	for range passes {
		var first []byte
		for i, c := range cases {
			b := run("list", list, append([]string{"--json"}, c...)...)
			if i == 0 {
				first = b
			}
		}
		run("stats", (*env).jobsStatsCmd, "--json", "--view", "new")
		run("stats", (*env).jobsStatsCmd, "--json", "--view", "all", "--q", "engineer")
		run("gaps", (*env).jobsGapsCmd, "--json", "--view", "new")

		var l struct {
			Jobs []struct {
				Key string `json:"key"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(first, &l); err != nil {
			t.Fatalf("list new: %v", err)
		}
		for i, j := range l.Jobs {
			if i >= 5 {
				break
			}
			run("show", (*env).jobsShow, "--json", j.Key)
		}
	}
	if err := os.WriteFile(out, dump.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
