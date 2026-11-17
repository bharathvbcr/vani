package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// w1dSeedClosed adds a dead and a duplicate record, text "Closed text <key>",
// to the ledger under stateDir. Like the dropped ones seedAudit writes they are
// closed, so a jobs read leaves their text out unless they are marked.
func w1dSeedClosed(t *testing.T, stateDir string, keys ...string) {
	t.Helper()
	l, err := ledger.Open(filepath.Join(stateDir, "ledger"))
	if err != nil {
		t.Fatal(err)
	}
	for i, k := range keys {
		p := posting.Posting{Key: posting.Key(k), ATS: "gh", Company: "Acme", Title: "Engineer " + k, Description: "Closed text " + k}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		st, ann := posting.Dead, map[string]string{}
		if i%2 == 1 {
			st, ann = posting.Duplicate, map[string]string{"duplicate_of": "gh:k:000"}
		}
		if err := l.Append(context.Background(), ledger.Event{V: 1, At: jobsT0, Key: p.Key, Status: st, Annotations: ann}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// Every card of a dropped or closed posting carries its text, and the label an
// answer writes holds the hash of that text, never the hash of the empty string.
func TestW1dAuditCardsCarryTheirText(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 4, 6, 0)
	w1dSeedClosed(t, state, "gh:c:000", "gh:c:001", "gh:c:002", "gh:c:003")

	old := stdin
	t.Cleanup(func() { stdin = old })
	stdin = strings.NewReader(strings.Repeat("1\n", 30))
	o, _ := mustCode(t, 0, append([]string{"audit", "--seed", "w1d", "--scored", "4", "--dropped", "6", "--closed", "4"}, noOverlays...)...)
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	labels, _, err := s.Labels()
	if err != nil || len(labels) == 0 {
		t.Fatalf("labels %d err %v\n%s", len(labels), err, o)
	}
	closedOrDropped := 0
	for k, e := range labels {
		text, err := s.Text(e.TextSHA256)
		if err != nil || e.TextSHA256 == eval.HashText("") {
			t.Fatalf("%s: stored text %q (%v), hash %s: the hash of empty text", k, text, err, e.TextSHA256)
		}
		want := "Build things. " + string(k)
		if strings.HasPrefix(string(k), "gh:c:") {
			want = "Closed text " + string(k)
		}
		if strings.HasPrefix(string(k), "gh:k:") {
			continue
		}
		closedOrDropped++
		if text != want {
			t.Errorf("%s: stored text %q, want %q", k, text, want)
		}
		if !strings.Contains(o, want) {
			t.Errorf("%s: the card does not show %q", k, want)
		}
	}
	if closedOrDropped == 0 {
		t.Fatal("no dropped or closed card was drawn")
	}
}

// An answer for a dropped posting whose text cannot be read back is refused and
// writes no label: it never stores the hash of empty text.
func TestW1dAuditAnswerRefusesUnreadableText(t *testing.T) {
	_, state := setup(t)
	mustCode(t, 0, "init")
	seedAudit(t, state, 2, 2, 0)

	// The resident reader folded the ledger before the line is edited, so it
	// remembers the fingerprint of each evicted body and catches the edit when
	// the body is read back.
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
	e := &env{resident: &jobsResident{rd: rd}}
	recs := []ledger.Record{{Posting: posting.Posting{Key: "gh:t:000"}, Status: posting.Dropped, BodyEvicted: true}}
	recs[0].Key = "gh:t:000"
	err = e.withText(recs)
	if !errors.Is(err, ledger.ErrBodyUnavailable) || !strings.Contains(err.Error(), "gh:t:000") {
		t.Fatalf("withText = %v, want ErrBodyUnavailable naming the posting", err)
	}
	if recs[0].Posting.Description != "" || !recs[0].BodyEvicted {
		t.Fatalf("an unreadable body was filled in: %+v", recs[0])
	}
	// The labelling loops refuse a bodiless record rather than hash its empty text.
	s, err := eval.NewStore(filepath.Join(state, "eval", "labels"), filepath.Join(state, "eval", "text"))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	pick := eval.AuditPick{Key: "gh:t:000", Source: "dropped:title:x", PSelect: 0.5}
	n, err := eval.LabelAudit(strings.NewReader("1\n"), &out, []eval.AuditPick{pick}, recs, s, eval.Names{}, false)
	if err == nil || n != 0 {
		t.Fatalf("LabelAudit wrote %d labels, err %v, for a bodiless record", n, err)
	}
	if labels, _, err := s.Labels(); err != nil || len(labels) != 0 {
		t.Fatalf("labels %d err %v after a refused answer", len(labels), err)
	}
}
