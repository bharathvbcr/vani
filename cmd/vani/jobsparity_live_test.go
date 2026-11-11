package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
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
		return stdout.Bytes()
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
	if err := os.WriteFile(out, dump.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
