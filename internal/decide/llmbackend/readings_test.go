package llmbackend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bharathvbcr/decide"
)

// The pipeline treats Unavailable and NotAsked as "nobody read the posting"
// (retried next run) and Failed as "a backend read it and produced something
// unusable" (an Unsure verdict). So a path that never got a reply must read
// Unavailable on every slot, and only a reply that was parsed may read Failed.
// llm.go's Decide: Turn errors -> Unavailable (every transport path below);
// parse -> Failed only for a reply that arrived and could not be used.
func TestNoReplyPathsAreUnavailableNeverFailed(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name string
		p    *fakeProvider
		ctx  context.Context
	}{
		{"provider refuses the request (HTTP error, budget refusal)", &fakeProvider{streamErr: errors.New("anthropic: 529 overloaded")}, context.Background()},
		{"budget exhausted at admission", &fakeProvider{streamErr: errors.New("attempt admission: budget exhausted")}, context.Background()},
		{"stream resets midway", &fakeProvider{nextErr: errors.New("connection reset")}, context.Background()},
		{"final response unreadable", &fakeProvider{text: "{}", respErr: errors.New("stream ended without a final message")}, context.Background()},
		{"caller context already done", &fakeProvider{streamErr: context.Canceled}, cancelled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := New(c.p, Config{Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			a, err := b.Decide(c.ctx, question())
			if err == nil {
				t.Fatal("a call that got no reply returned no error")
			}
			if len(a.Slots) != 2 {
				t.Fatalf("slots %+v", a.Slots)
			}
			for name, rd := range a.Slots {
				if rd.Reading != decide.Unavailable {
					t.Errorf("%s = %+v, want unavailable", name, rd)
				}
			}
		})
	}

	// A deadline is Unavailable too, with its own detail.
	b, _ := New(&fakeProvider{block: true}, Config{Model: "m", Timeout: 20 * time.Millisecond})
	a, err := b.Decide(context.Background(), question())
	if err == nil || a.Slots["mode"].Reading != decide.Unavailable || a.Slots["mode"].Detail != "deadline" {
		t.Errorf("deadline: err %v, %+v", err, a.Slots["mode"])
	}
}

// A reply that arrived but cannot be used is Failed, on every slot it spoils,
// and is not an error: it is a reading of this posting.
func TestUnusableReplyIsFailedNotUnavailable(t *testing.T) {
	cases := map[string]string{
		"not json":             "I think it is remote.",
		"trailing data":        `{"mode":"remote","level":"senior"} extra`,
		"empty reply":          "",
		"value not in options": `{"mode":"hybrid","level":"senior"}`,
		"slot missing":         `{"mode":"remote"}`,
		"not a string":         `{"mode":1,"level":"senior"}`,
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			a := decideWith(t, reply)
			failed := 0
			for slot, rd := range a.Slots {
				switch rd.Reading {
				case decide.Failed:
					failed++
				case decide.Answered:
				default:
					t.Errorf("%s = %+v: an unusable reply must read failed, never %s", slot, rd, rd.Reading)
				}
			}
			if failed == 0 {
				t.Errorf("no slot failed: %+v", a.Slots)
			}
		})
	}
}
