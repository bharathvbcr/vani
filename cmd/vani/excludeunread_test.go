package main

import (
	"context"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The run's exclusion pass keeps a Live posting whose location a location rule
// could not read, and says so on the fail-open counter, so the digest does not
// present it as having passed the rule.
func TestApplyExclusionsCountsUnreadableLocation(t *testing.T) {
	cfg := config.Default()
	cfg.Exclude = config.Exclude{Locations: []string{"London"}, OnlyLocations: []string{"United States"}}
	w, _ := openWired(t, cfg)
	for key, loc := range map[string]string{"gh:a:1": "", "gh:a:2": "Austin, TX", "gh:a:3": "London, UK"} {
		p := posting.Posting{Key: posting.Key(key), ATS: "gh", Company: "Acme", Title: "Engineer " + key, Location: loc}
		if err := w.led.Append(context.Background(), ledger.Event{V: 1, At: time.Now(), Key: p.Key, Status: posting.Live, Reason: "new", Posting: &p}); err != nil {
			t.Fatal(err)
		}
	}
	fo := &pipeline.FailOpen{}
	if err := w.applyExclusions(context.Background(), dropPolicy(cfg, ""), nil, time.Now(), fo); err != nil {
		t.Fatal(err)
	}
	if got := fo.Counts(); got.LocationUnreadable != 1 || got.Total() != 1 {
		t.Fatalf("fail-open %+v, want exactly the blank-location posting counted", got)
	}
	if r, _ := w.led.Get("gh:a:1"); r.Status != posting.Live {
		t.Errorf("the unreadable posting is %s, want it kept Live", r.Status)
	}
	if r, _ := w.led.Get("gh:a:3"); r.Status != posting.Dropped {
		t.Errorf("the London posting is %s, want it dropped", r.Status)
	}
}
