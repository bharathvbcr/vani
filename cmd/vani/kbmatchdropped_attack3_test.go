package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bharathvbcr/decide"
)

// The run stops on an outage (an error exit) after one answer was kept into a
// store with no room: the dropped line still appears exactly once, and under
// --json stdout stays one valid JSON object.
func TestAttackKBMatchDroppedLineOnTheOutageExit(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", asJSON), func(t *testing.T) {
			k := kbmSetup(t,
				kbmSeed{"gh:a:1", "A", "First", longDesc, "partial_fit"},
				kbmSeed{"gh:a:2", "B", "Second", longDesc, "partial_fit"},
				kbmSeed{"gh:a:3", "C", "Third", longDesc, "partial_fit"},
				kbmSeed{"gh:a:4", "D", "Fourth", longDesc, "partial_fit"},
			)
			seeded := tightKBMatchStore(t, k.state)
			n := 0
			useFake(t, &fakeKB{answer: func(string) decide.SlotReading {
				if n++; n == 1 {
					return decide.SlotReading{Reading: decide.Answered, Value: "partial"}
				}
				return decide.SlotReading{Reading: decide.Unavailable, Detail: "budget"}
			}})
			args := []string{"kbmatch", "run"}
			if asJSON {
				args = []string{"--json", "kbmatch", "run"}
			}
			code, o, e := do(t, args...)
			if code != exitRuntime {
				t.Fatalf("exit %d\n%s\n%s", code, o, e)
			}
			dropped := seeded + 1 - storedKBMatch(t, k.state)
			if dropped < 1 {
				t.Fatalf("setup: nothing dropped (seeded %d)", seeded)
			}
			line := fmt.Sprintf("kbmatch: dropped %d oldest answer(s)", dropped)
			if got := strings.Count(o+e, "oldest answer(s)"); got != 1 {
				t.Fatalf("dropped line printed %d times:\nstdout %s\nstderr %s", got, o, e)
			}
			if !strings.Contains(o+e, line) {
				t.Fatalf("want %q:\nstdout %s\nstderr %s", line, o, e)
			}
			if asJSON {
				if strings.Contains(o, "oldest answer(s)") {
					t.Fatalf("the line leaked onto stdout: %s", o)
				}
				var v map[string]any
				if err := json.Unmarshal([]byte(o), &v); err != nil {
					t.Fatalf("stdout is not one JSON object: %v\n%s", err, o)
				}
			}
		})
	}
}
