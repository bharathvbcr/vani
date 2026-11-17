package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A draw of many picks drops the pick whose text cannot be read back and names
// it with the reason; the rest are shown, aligned with their records. One key
// asked for alone (withText, as answerAudit uses it) still fails closed.
func TestW1dAuditDrawDropsAndNamesAnUnreadablePick(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 2, 3, 0)

	rd, err := ledger.OpenReader(filepath.Join(state, "ledger"), ledger.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rd.Close() })
	path := filepath.Join(state, "ledger", "events.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const text, garbled = "Build things. gh:t:000", "XXXXX XXXXXX. XX:X:XXX"
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(b), text, garbled)), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	e := &env{resident: &jobsResident{rd: rd}, stdout: &out}
	mk := func(k posting.Key) ledger.Record {
		return ledger.Record{Key: k, Posting: posting.Posting{Key: k}, Status: posting.Dropped, BodyEvicted: true}
	}
	picks := []eval.AuditPick{{Key: "gh:t:000", Source: "dropped:a"}, {Key: "gh:t:001", Source: "dropped:b"}, {Key: "gh:t:002", Source: "dropped:c"}}
	recs := []ledger.Record{mk("gh:t:000"), mk("gh:t:001"), mk("gh:t:002")}

	// A single key is fail-closed, and fills in nothing it could not read.
	one := []ledger.Record{recs[0]}
	if err := e.withText(one); !errors.Is(err, ledger.ErrBodyUnavailable) || !one[0].BodyEvicted {
		t.Fatalf("withText = %v, evicted %v: want a refusal naming the posting", err, one[0].BodyEvicted)
	}

	kp, kr, err := e.dropUnreadable(picks, recs)
	if err != nil {
		t.Fatal(err)
	}
	if len(kp) != 2 || len(kr) != 2 || kp[0].Key != "gh:t:001" || kp[1].Key != "gh:t:002" {
		t.Fatalf("kept picks %+v, want gh:t:001 and gh:t:002", kp)
	}
	for i := range kp {
		if kr[i].Key != kp[i].Key || kr[i].BodyEvicted || kr[i].Posting.Description != "Build things. "+string(kp[i].Key) {
			t.Fatalf("record %d %+v is not its pick's, with text", i, kr[i])
		}
	}
	if got := out.String(); !strings.Contains(got, "pick not shown: gh:t:000") || !strings.Contains(got, "cannot be read back") {
		t.Fatalf("the dropped pick is not named with its reason:\n%s", got)
	}
}

// With nothing unreadable the draw passes through unchanged and says nothing;
// with every pick unreadable nothing is kept and every one is named.
func TestW1dAuditDropUnreadableEdges(t *testing.T) {
	var out strings.Builder
	e := &env{stdout: &out}
	picks := []eval.AuditPick{{Key: "gh:t:000"}}
	recs := []ledger.Record{{Key: "gh:t:000", Posting: posting.Posting{Key: "gh:t:000", Description: "has text"}}}
	kp, kr, err := e.dropUnreadable(picks, recs)
	if err != nil || len(kp) != 1 || len(kr) != 1 || out.Len() != 0 {
		t.Fatalf("readable draw: %v %v %v, output %q", kp, kr, err, out.String())
	}
	if kp, kr, err := e.dropUnreadable(nil, nil); err != nil || len(kp) != 0 || len(kr) != 0 {
		t.Fatalf("empty draw: %v %v %v", kp, kr, err)
	}
}
