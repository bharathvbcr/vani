package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/feedback"
)

// `audit answer --shown` is the one-tap answer from a surface row: it takes the
// stratum and p_select of the posting's newest sampled impression (an Explore
// pick), and records a manual label when the posting was never sampled.
func TestAuditAnswerShownTakesTheImpressionsStratum(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 12, 2, 2)
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	rows := []feedback.Impression{
		// An older Explore pick, then a newer one: the newer stratum wins.
		{At: at, SlateID: "2026-11-01", Surface: feedback.SurfaceDigest, Section: "explore", Key: "gh:k:003", Position: 21, Source: "decile:2", PSelect: 0.25},
		{At: at.Add(24 * time.Hour), SlateID: "2026-11-02", Surface: feedback.SurfaceDigest, Section: "explore", Key: "gh:k:003", Position: 22, Source: "decile:3", PSelect: 0.125},
		// A Jobs row: shown, never sampled.
		{At: at, SlateID: "jobs", Surface: feedback.SurfaceJobs, Key: "gh:k:004", Position: 1},
	}
	if err := appendImpressionRows(context.Background(), state, rows); err != nil {
		t.Fatal(err)
	}

	o, _ := mustCode(t, 0, "audit", "answer", "--json", "--shown", "--key=gh:k:003", "--verdict=yes")
	var got auditAnswerJSON
	if err := json.Unmarshal([]byte(o), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stratum != "decile:3" || got.PSelect != 0.125 || got.Verdict != "yes" {
		t.Errorf("sampled row: %+v", got)
	}
	o, _ = mustCode(t, 0, "audit", "answer", "--json", "--shown", "--key=gh:k:004", "--verdict=no")
	got = auditAnswerJSON{}
	if err := json.Unmarshal([]byte(o), &got); err != nil {
		t.Fatal(err)
	}
	if got.Stratum != eval.SourceManual || got.PSelect != 0 {
		t.Errorf("unsampled row must be manual: %+v", got)
	}
	// A second tap on the same posting is refused, not counted twice.
	if _, e := mustCode(t, 1, "audit", "answer", "--shown", "--key=gh:k:004", "--verdict=yes"); !strings.Contains(e, "already answered") {
		t.Errorf("repeat tap: %s", e)
	}
	// --shown never mixes with an explicit stratum or a draw seed.
	for _, extra := range [][]string{{"--source=decile:1", "--p-select=0.5"}, {"--seed=w1"}} {
		args := append([]string{"audit", "answer", "--shown", "--key=gh:k:005", "--verdict=no"}, extra...)
		if _, e := mustCode(t, 2, args...); !strings.Contains(e, "--shown") {
			t.Errorf("%v: %s", extra, e)
		}
	}
}

func TestAuditAnswerShownRefusesAnUnreadableImpressionLog(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 4, 0, 0)
	imp := filepath.Join(state, "feedback", feedback.ImpressionsFile)
	if err := os.MkdirAll(imp, 0o755); err != nil {
		t.Fatal(err)
	}
	// Guessing "manual" here would lose a real stratum: it is an error instead.
	if _, e := mustCode(t, 1, "audit", "answer", "--shown", "--key=gh:k:001", "--verdict=yes"); !strings.Contains(e, "read impressions") {
		t.Errorf("unreadable log: %s", e)
	}
}
