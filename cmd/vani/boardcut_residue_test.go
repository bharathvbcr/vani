package main

import (
	"errors"
	"os"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// A run limited to one board (--board) hands the app a cut watchlist. The next
// reload must not leave the app with that cut, whether the file reads or not:
// a reload that falls back to the last applied config (the file is invalid or
// gone) still applies the whole watchlist, not the previous run's cut.
func TestReloadAfterABoardRunLeavesTheWholeWatchlist(t *testing.T) {
	for name, damage := range map[string]func(t *testing.T, path string){
		"file invalid": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"file removed": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		},
		"file unchanged": nil,
	} {
		t.Run(name, func(t *testing.T) {
			e, cfgPath, start, _ := advEnv(t)
			start.Watchlist = []sources.Board{{ATS: posting.Greenhouse, Token: "a"}, {ATS: posting.Greenhouse, Token: "b"}}
			start.AllowedHosts = append(start.AllowedHosts, "boards-api.greenhouse.io")
			writeConfig(t, cfgPath, start)
			w, app := lifecycleApp(t, start)

			if _, err := e.reloadConfig(app, start, []string{"gh:a"}); err != nil {
				t.Fatal(err)
			}
			if got := len(w.cfg.Watchlist); got != 1 {
				t.Fatalf("control: the --board run holds %d boards, want 1", got)
			}
			if damage != nil {
				damage(t, cfgPath)
			}
			_, err := e.reloadConfig(app, start, nil)
			if damage != nil && !errors.Is(err, errConfigStale) {
				t.Fatalf("err = %v, want errConfigStale", err)
			}
			if damage == nil && err != nil {
				t.Fatal(err)
			}
			if got := len(w.cfg.Watchlist); got != 2 {
				t.Fatalf("after the next reload the app holds %d boards, want the whole watchlist (2): %+v", got, w.cfg.Watchlist)
			}
		})
	}
}
