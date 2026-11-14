package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/semantic"
)

// A warm's score evictions reach the run record and its counts line, and a
// browsable eviction says the cache is too small rather than passing silently.
func TestSemanticReportScoreEvictions(t *testing.T) {
	for _, tc := range []struct {
		name          string
		evicted, live int
		want, notWant []string
		wantJSON      []string
	}{
		{name: "none", notWant: []string{"evicted", "browsable"}},
		{name: "dead only", evicted: 7, want: []string{"evicted 7 cached score(s)"}, notWant: []string{"browsable"},
			wantJSON: []string{`"scores_evicted":7`}},
		{name: "live", evicted: 9, live: 4, want: []string{"evicted 9 cached score(s)", "4 still browsable", "scored again"},
			wantJSON: []string{`"scores_evicted":9`, `"scores_evicted_live":4`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s semanticReport
			s.fromWarm(semantic.Report{Scored: 3, Cached: 2, ScoresEvicted: tc.evicted, ScoresEvictedLive: tc.live})
			if s.ScoresEvicted != tc.evicted || s.ScoresEvictedLive != tc.live || s.Scored != 3 || s.Cached != 2 {
				t.Fatalf("fromWarm copied %+v", s)
			}
			line := s.countsLine()
			if !strings.HasPrefix(line, "scored 3, cached 2, failed 0, skipped 0") {
				t.Errorf("counts line %q lost its head", line)
			}
			for _, w := range tc.want {
				if !strings.Contains(line, w) {
					t.Errorf("counts line %q lacks %q", line, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(line, w) {
					t.Errorf("counts line %q has %q", line, w)
				}
			}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.wantJSON {
				if !strings.Contains(string(raw), w) {
					t.Errorf("record %s lacks %s", raw, w)
				}
			}
			if tc.evicted == 0 && strings.Contains(string(raw), "scores_evicted") {
				t.Errorf("record %s names evictions that did not happen", raw)
			}
		})
	}
}
