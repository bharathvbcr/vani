package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// An empty or comment-only words file is a words check that ran against zero
// personal terms. It must not look like a completed personal-name check.
func TestAdversarialTrackEmptyWordsFileIsNotAPersonalCheck(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "comments": "# nothing yet\n\n   \n"} {
		t.Run(name, func(t *testing.T) {
			cfgPath, dir := trackSetup(t)
			trackBank(t, cfgPath)
			if err := os.WriteFile(filepath.Join(dir, trackWordsFile()), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, o, _ := do(t, "track", "show", "ml-systems")
			if strings.Contains(o, "check: ok") {
				t.Errorf("a words file with no terms reports check: ok (personal names were not checked against anything):\n%s", o)
			}
		})
	}
}

// A key a concurrent writer dropped (never scored) must not be counted as rescored.
func TestAdversarialRescoreCountsOnlyScoredKeys(t *testing.T) {
	l := rescoreLedger(t, []rescoreSeed{
		{"1", posting.Explained, "jd", ""},
		{"2", posting.Explained, "jd", ""},
	})
	at := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	inner := rescoreRunner(l, noMustHaveScorer{}).Rescore
	rescore := func(ctx context.Context, k posting.Key) error {
		if strings.HasSuffix(string(k), ":2") {
			r, _ := l.Get(k)
			return l.Append(ctx, ledger.Event{V: 1, At: at, Key: k, Status: posting.Dropped, Posting: &r.Posting})
		}
		return inner(ctx, k)
	}
	rep, err := rescoreWith(context.Background(), l, 10, 1, rescore, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rescored != 1 {
		t.Errorf("rescored %d, want 1: a dropped, never-scored posting is counted as rescored (command prints eligible-rescored as \"still unscored\")", rep.Rescored)
	}
}
