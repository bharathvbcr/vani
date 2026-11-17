package workable

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// A stored posting that is still on the widget list and is due its re-read, whose
// detail answers 404 (a CDN blip looks the same as a closed job): is it listed as
// stored (held) or left out (aged as absent)?
func TestW4cadvStoredDueRowThatAnswers404IsNotLeftOutAsAbsent(t *testing.T) {
	ids := w4cIDs(30)
	on, _ := w4cTurns(ids)
	if len(on) == 0 {
		t.Fatal("no id is on its turn")
	}
	sp := w4cStore(t, on[0])
	r := replies(ids...)
	r[detailURL(on[0])] = srctest.Reply{Status: 404, Body: []byte(`{}`)}
	var st Stats
	_, d := newSource(r)
	got, err := w4cBounded(d, Options{
		Stored: func(sources.Board) []posting.Posting { return []posting.Posting{sp} },
		Report: func(_ sources.Board, x Stats) { st = x },
	}).List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		if p.Key == sp.Key {
			return
		}
	}
	t.Fatalf("stored posting %s is on the widget list but absent from the listing (stats %+v): it will be aged as absent", sp.Key, st)
}
