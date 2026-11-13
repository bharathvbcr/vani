package main

import (
	"path/filepath"
	"strings"

	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// A board whose host was rate limited when its turn came is left unchanged and
// tried again next run (never aged: only a read board is reconciled). Tried in
// the same order every run, the boards at the back of a shared host's queue
// would be the ones refused every day, so the run records which boards were
// rate limited (srcutil.SaveStarved) and the next one tries them first.

// starvedFile is where the starved boards are kept, beside the host cooldowns;
// empty when there is no cooldown file, which keeps the memory to the run.
func starvedFile(cooldownFile string) string {
	if cooldownFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cooldownFile), "starved.json")
}

// starvedKey names a board in the starved file and in a board report: its ATS
// and its token as the report writes it, lower-cased.
func starvedKey(ats, token string) string {
	return strings.ToLower(ats) + ":" + strings.ToLower(strings.TrimSpace(oneLine(token, maxBoardNameRunes)))
}

func boardStarvedKey(b sources.Board) string { return starvedKey(string(b.ATS), b.Token) }

// starvedFirst is watchlist with the boards named in keys moved to the front,
// each group in watchlist order. It returns watchlist itself when nothing
// moves, and never changes it.
func starvedFirst(watchlist []sources.Board, keys []string) []sources.Board {
	if len(keys) == 0 {
		return watchlist
	}
	starved := make(map[string]bool, len(keys))
	for _, k := range keys {
		starved[k] = true
	}
	front := make([]sources.Board, 0, len(watchlist))
	var rest []sources.Board
	for _, b := range watchlist {
		if starved[boardStarvedKey(b)] {
			front = append(front, b)
		} else {
			rest = append(rest, b)
		}
	}
	if len(front) == 0 || len(rest) == 0 {
		return watchlist
	}
	return append(front, rest...)
}

// nextStarved is what to leave for the next run: the boards this run rate
// limited, then the earlier run's starved boards this run never reached (they
// have not had their turn), each once. A board this run tried and did not rate
// limit (read, or failed another way) has had its turn and drops off.
func nextStarved(prior []string, tried []boardReport) []string {
	var out []string
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	reached := make(map[string]bool, len(tried))
	for _, e := range tried {
		k := starvedKey(e.ATS, e.Token)
		reached[k] = true
		if e.Outcome == boardRateLimited {
			add(k)
		}
	}
	for _, k := range prior {
		if !reached[k] {
			add(k)
		}
	}
	return out
}

// openStarved loads the boards the last run left starved and returns the
// function that writes this run's. Nothing is written when path is empty or
// nothing changed. A file that cannot be used is a warning, not a failed run,
// and the write replaces it.
func openStarved(path string, warnf func(string, ...any)) (prior []string, save func(tried []boardReport)) {
	var loadWarn string
	if path != "" {
		prior, loadWarn = srcutil.LoadStarved(path, boardsClock())
		if loadWarn != "" {
			warnf("%s", loadWarn)
		}
	}
	return prior, func(tried []boardReport) {
		if path == "" {
			return
		}
		next := nextStarved(prior, tried)
		if loadWarn == "" && equalKeys(prior, next) {
			return
		}
		if err := srcutil.SaveStarved(path, next, boardsClock()); err != nil {
			warnf("starved boards not saved: %v", err)
		}
	}
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
