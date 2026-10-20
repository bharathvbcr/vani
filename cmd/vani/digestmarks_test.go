package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The digest honours the Jobs tab: a role hidden there is left out of the
// digest and stays pending (not Digested), and a saved role is listed under
// Saved. The note never reaches the digest.
func TestDigestLeavesOutHiddenAndListsSaved(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	l, n := fixture(), &fakeNotifier{}
	wire(t, l, n, nil)
	dir := marks.Dir(stateDir)
	at := time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)
	if _, err := marks.SetMark(dir, "gh:a:1", marks.Hidden, at); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetMark(dir, "gh:a:2", marks.Saved, at); err != nil {
		t.Fatal(err)
	}
	if _, err := marks.SetNote(dir, "gh:a:2", "private-note-canary", at); err != nil {
		t.Fatal(err)
	}

	o, _ := mustCode(t, 0, "run-once")
	if !strings.Contains(o, "(1 roles)") {
		t.Errorf("stdout %q, want 1 role", o)
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "digests", "2026-10-18.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(data)
	if strings.Contains(md, "x.example/gh:a:1>") {
		t.Errorf("hidden role in the digest:\n%s", md)
	}
	if !strings.Contains(md, "## Saved (1)") || !strings.Contains(md, "x.example/gh:a:2") {
		t.Errorf("saved role not listed under Saved:\n%s", md)
	}
	if strings.Contains(md, "private-note-canary") {
		t.Errorf("note reached the digest:\n%s", md)
	}
	if r, _ := l.Get("gh:a:1"); r.Status != posting.Explained {
		t.Errorf("hidden role status %s, want explained (left pending)", r.Status)
	}
	if r, _ := l.Get("gh:a:2"); r.Status != posting.Digested {
		t.Errorf("listed role status %s, want digested", r.Status)
	}
	if len(n.calls) != 1 || !strings.Contains(n.calls[0], "|1 role to review") {
		t.Errorf("notifications: %v", n.calls)
	}

	// Unhiding puts the role back in the next digest.
	if _, err := marks.SetMark(dir, "gh:a:1", marks.None, at); err != nil {
		t.Fatal(err)
	}
	o, _ = mustCode(t, 0, "run-once")
	if !strings.Contains(o, "(1 roles)") {
		t.Errorf("after unhide: stdout %q", o)
	}
	if r, _ := l.Get("gh:a:1"); r.Status != posting.Digested {
		t.Errorf("unhidden role status %s, want digested", r.Status)
	}
}

// A marks store the digest cannot read holds the digest back: no file, no
// Digested events, a loud error and a notification. It never falls back to
// "no marks", which would list (and consume) roles the user hid. The
// pipeline's own results are already in the ledger and are not undone.
func TestDigestHeldBackWhenMarksUnreadable(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"corrupt": func(t *testing.T, dir string) {
			if _, err := marks.SetMark(dir, "gh:a:1", marks.Hidden, time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			// A bad line before an intact one is corruption, not a torn tail.
			f := filepath.Join(dir, "marks.jsonl")
			good, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f, append([]byte("{not json\n"), good...), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"lock removed": func(t *testing.T, dir string) {
			if _, err := marks.SetMark(dir, "gh:a:1", marks.Hidden, time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, "lock")); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable": func(t *testing.T, dir string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a 0000 file")
			}
			if _, err := marks.SetMark(dir, "gh:a:1", marks.Hidden, time.Date(2026, 10, 18, 14, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			f := filepath.Join(dir, "marks.jsonl")
			if err := os.Chmod(f, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(f, 0o600) })
		},
	}
	for name, breakStore := range cases {
		t.Run(name, func(t *testing.T) {
			_, stateDir := setup(t)
			mustCode(t, 0, "init")
			l, n := fixture(), &fakeNotifier{}
			runs := wire(t, l, n, nil)
			breakStore(t, marks.Dir(stateDir))

			_, e := mustCode(t, 1, "run-once")
			if *runs != 1 {
				t.Errorf("pipeline ran %d times, want 1", *runs)
			}
			if !strings.Contains(e, "digest held back") || !strings.Contains(e, "marks") {
				t.Errorf("stderr %q does not name the held digest and the marks store", e)
			}
			if _, err := os.Stat(filepath.Join(stateDir, "digests")); !os.IsNotExist(err) {
				t.Errorf("digest written with unreadable marks: %v", err)
			}
			for _, k := range []posting.Key{"gh:a:1", "gh:a:2"} {
				if r, _ := l.Get(k); r.Status == posting.Digested {
					t.Errorf("%s marked digested with unreadable marks", k)
				}
			}
			if len(n.calls) != 1 || !strings.Contains(n.calls[0], "held back") {
				t.Errorf("notifications: %v", n.calls)
			}
		})
	}
}
