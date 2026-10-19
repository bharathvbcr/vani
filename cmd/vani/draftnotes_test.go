package main

import (
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

func TestDraftPassesMotivationNotesThrough(t *testing.T) {
	const notes = "I want my work to make AI systems safer to deploy."
	for _, tc := range []struct {
		name, req string
		want      bool
	}{
		{"motivation question", `{"question":"Why Modal?","job":{"company":"Modal","title":"Engineer","description":"Latency matters."}}`, true},
		{"other question", `{"question":"Describe a time you cut latency.","job":{"company":"Modal","title":"Engineer","description":"Latency matters."}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &draftLLM{text: "A draft."}
			draftSetup(t, tc.req, p, false)
			cfgPath := os.Getenv("VANI_CONFIG")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.MotivationNotes = notes
			if err := config.Save(cfgPath, cfg); err != nil {
				t.Fatal(err)
			}
			stdout, _ := mustCode(t, 0, "draft")
			if r := draftOut(t, stdout); r.Error != "" {
				t.Fatalf("reply = %+v", r)
			}
			if got := strings.Contains(p.reqs[0].Messages[0].Text(), notes); got != tc.want {
				t.Errorf("notes in prompt = %v, want %v", got, tc.want)
			}
		})
	}
}
