package main

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// Fifty `watchlist add` calls racing on one config: each is a load-modify-write,
// and without one locked owner the later write replaces the earlier ones.
func TestConcurrentWatchlistAddsAllSurvive(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := &env{configPath: cfgPath, stateDir: t.TempDir(), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
			errs[i] = e.watchlistAdd([]string{"--ats", "gh", "--token", fmt.Sprintf("board%02d", i), "--company", "Co", "--allow-host"})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("add %d: %v", i, err)
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, b := range cfg.Watchlist {
		have[b.Token] = true
	}
	for i := 0; i < n; i++ {
		if tok := fmt.Sprintf("board%02d", i); !have[tok] {
			t.Errorf("board %s was lost (%d of %d on the watchlist)", tok, len(cfg.Watchlist), n)
		}
	}
}

// `config check` must refuse what the run refuses: egress.New fails the whole
// run on one bad allowed_hosts entry.
func TestValidateNamesUnusableAllowedHosts(t *testing.T) {
	cfgPath, _ := setup(t)
	mustCode(t, 0, "init")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	idx := fmt.Sprintf("allowed_hosts[%d]", len(cfg.AllowedHosts))
	for _, bad := range []string{"https://x.example", "*.com", "10.0.0.5", "example.com:443", "a.example/path", "HTTPS://X.example", ""} {
		c := cfg
		c.AllowedHosts = append(slices.Clone(cfg.AllowedHosts), bad)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), idx) {
			t.Errorf("Validate(%q) = %v, want an error naming %s", bad, err, idx)
		}
	}
}
