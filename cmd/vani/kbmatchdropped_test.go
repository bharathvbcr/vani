package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/kbmatch"
)

// fatKBMatchStore saves a store of n answers at the byte limit (Save trims it to
// fit), with key padding of padLen, and returns what the file holds and the room
// left under the limit.
func fatKBMatchStore(t *testing.T, state string, n, padLen int) (held, slack int) {
	t.Helper()
	path := filepath.Join(state, kbmatch.FileName)
	s := kbmatch.NewStore(path)
	pad := strings.Repeat("k", padLen)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		e := kbmatch.Entry{Value: "core", Model: strings.Repeat("m", 128), KB: strings.Repeat("a", 64), Text: strings.Repeat("b", 64), At: at.Add(time.Duration(i) * time.Second)}
		if err := s.Put(fmt.Sprintf("%s:%08d", pad, i), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := kbmatch.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return back.Len(), kbmatch.MaxFileBytes - int(info.Size())
}

// tightKBMatchStore is a store at the limit with less room left than two of the
// run's answers need, so they cannot both be saved without a drop.
func tightKBMatchStore(t *testing.T, state string) int {
	t.Helper()
	for padLen := 500; padLen > 440; padLen-- {
		if held, slack := fatKBMatchStore(t, state, kbmatch.MaxEntries/2, padLen); slack < 200 {
			return held
		}
	}
	t.Fatal("setup: no padding left the store within 200 bytes of the limit")
	return 0
}

func storedKBMatch(t *testing.T, state string) int {
	t.Helper()
	back, err := kbmatch.OpenStore(filepath.Join(state, kbmatch.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return back.Len()
}

// A run whose answers push the store over its byte limit says how many of the
// oldest answers Save let go, in the text and in the JSON result.
func TestKBMatchRunSaysWhenTheStoreDroppedAnswers(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%v", asJSON), func(t *testing.T) {
			k := twoPostings(t)
			seeded := tightKBMatchStore(t, k.state)
			useFake(t, &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})})
			args := []string{"kbmatch", "run"}
			if asJSON {
				args = []string{"--json", "kbmatch", "run"}
			}
			o, errOut := mustCode(t, 0, args...)
			dropped := seeded + 2 - storedKBMatch(t, k.state)
			if dropped < 1 {
				t.Fatalf("setup: the store dropped nothing (seeded %d)", seeded)
			}
			line := fmt.Sprintf("kbmatch: dropped %d oldest answer(s) to keep the store under 8 MiB", dropped)
			if asJSON {
				var r kbmatchRunReply
				decodeOne(t, o, &r)
				if r.Result == nil || r.Result.Dropped != dropped {
					t.Errorf("result.dropped: want %d in %s", dropped, o)
				}
				if !strings.Contains(errOut, line) || strings.Contains(o, "oldest answer(s)") {
					t.Errorf("the line belongs on stderr only:\nstdout %s\nstderr %s", o, errOut)
				}
			} else if !strings.Contains(o, line) {
				t.Errorf("want %q in:\n%s", line, o)
			}
		})
	}
}

// A run that fits says nothing about dropping and carries no field for it.
func TestKBMatchRunSaysNothingWhenNothingWasDropped(t *testing.T) {
	k := twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})})
	o, errOut := mustCode(t, 0, "kbmatch", "run")
	if strings.Contains(o+errOut, "dropped") {
		t.Errorf("unexpected drop line:\n%s\n%s", o, errOut)
	}
	k = twoPostings(t)
	useFake(t, &fakeKB{answer: byTitle(map[string]string{"Inference Engineer": "core", "Marketing Lead": "unrelated"})})
	o, _ = mustCode(t, 0, "--json", "kbmatch", "run")
	if strings.Contains(o, "dropped") {
		t.Errorf("json carries a dropped field with nothing dropped: %s", o)
	}
	_ = k
}
